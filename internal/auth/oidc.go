package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OIDC support: the Authorization Code flow with PKCE, against any compliant
// provider (Entra ID, Google, Keycloak, Okta...).
//
// It is written against the standard library rather than an OIDC package. The
// flow is small, the parts that matter are the validation rules, and those are
// exactly the parts worth reading in a security review rather than trusting to a
// dependency. See docs/adr/0006-authentication-model.md.

// OIDCConfig is what an operator supplies.
type OIDCConfig struct {
	// Issuer is the provider's base URL, e.g. https://login.example.com/tenant.
	Issuer string
	// ClientID and ClientSecret identify this application to the provider. The
	// secret is optional: a provider configured for a public client with PKCE
	// does not need one.
	ClientID     string
	ClientSecret string
	// RedirectURL must match what is registered with the provider exactly.
	RedirectURL string
	// Scopes always include openid; email and profile are added by default so
	// that a new account has a name to display.
	Scopes []string
	// RoleClaim, when set, names a claim mapped to an application role.
	RoleClaim string
	// RoleMapping maps claim values to roles, e.g. {"timetracker-admins":"admin"}.
	RoleMapping map[string]string
}

// discoveryDocument is the subset of the provider metadata we use.
type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// OIDCProvider is a configured, discovered provider.
type OIDCProvider struct {
	config    OIDCConfig
	discovery discoveryDocument
	client    *http.Client

	// keys caches the provider's signing keys. Providers rotate them, so the
	// cache has a lifetime and a miss triggers a refetch - a token signed with a
	// key we have not seen is the normal signal that rotation happened, not an
	// attack.
	mu          sync.RWMutex
	keys        map[string]*rsa.PublicKey
	keysFetched time.Time
}

const jwksCacheTTL = time.Hour

// NewOIDCProvider fetches the provider's discovery document and prepares it.
//
// Discovery happens once at startup rather than per login: a provider that is
// unreachable should stop the operator at start-up, when they are watching, and
// not at the moment a user tries to sign in.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig, client *http.Client) (*OIDCProvider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("OIDC requires an issuer, a client id and a redirect URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}

	// The well-known path is appended to the issuer, per the specification.
	metadataURL := strings.TrimSuffix(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OIDC discovery document: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned %s", resp.Status)
	}

	var doc discoveryDocument
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse OIDC discovery document: %w", err)
	}
	// The issuer inside the document must match the one we asked about,
	// otherwise a redirect could have pointed us at a different provider's
	// metadata while we believe we are talking to the configured one.
	if doc.Issuer != strings.TrimSuffix(cfg.Issuer, "/") && doc.Issuer != cfg.Issuer {
		return nil, fmt.Errorf("OIDC issuer mismatch: configured %q, document says %q",
			cfg.Issuer, doc.Issuer)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		return nil, errors.New("OIDC discovery document is missing required endpoints")
	}

	return &OIDCProvider{config: cfg, discovery: doc, client: client, keys: map[string]*rsa.PublicKey{}}, nil
}

// AuthRequest is the per-login state that must survive the round trip to the
// provider and back. It is held in a short-lived cookie, not in the URL.
type AuthRequest struct {
	// State is echoed back by the provider and compared; it is what ties the
	// callback to a login this browser actually started, and is the defence
	// against a cross-site request forgery on the login flow itself.
	State string
	// Nonce is embedded in the ID token by the provider and compared; it binds
	// the token to this request and prevents replay of a token obtained
	// elsewhere.
	Nonce string
	// Verifier is the PKCE code verifier. Its hash goes out with the
	// authorisation request and the verifier itself with the token exchange, so
	// an intercepted authorisation code is useless without it.
	Verifier string
	Created  time.Time
}

// NewAuthRequest generates the state, nonce and PKCE verifier for one login.
func NewAuthRequest() (AuthRequest, error) {
	values := make([]string, 3)
	for i := range values {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return AuthRequest{}, fmt.Errorf("generate OIDC request state: %w", err)
		}
		values[i] = base64.RawURLEncoding.EncodeToString(raw)
	}
	return AuthRequest{State: values[0], Nonce: values[1], Verifier: values[2], Created: time.Now()}, nil
}

// AuthorizationURL builds the URL to send the browser to.
func (p *OIDCProvider) AuthorizationURL(req AuthRequest) string {
	// S256 rather than the "plain" PKCE method: plain sends the verifier itself
	// through the browser, which defeats the purpose.
	challenge := sha256.Sum256([]byte(req.Verifier))

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", p.config.ClientID)
	params.Set("redirect_uri", p.config.RedirectURL)
	params.Set("scope", strings.Join(p.config.Scopes, " "))
	params.Set("state", req.State)
	params.Set("nonce", req.Nonce)
	params.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	params.Set("code_challenge_method", "S256")

	return p.discovery.AuthorizationEndpoint + "?" + params.Encode()
}

// Claims are the identity facts taken from a validated ID token.
type Claims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Issuer        string `json:"iss"`
	Audience      any    `json:"aud"`
	Expiry        int64  `json:"exp"`
	IssuedAt      int64  `json:"iat"`
	Nonce         string `json:"nonce"`
	// raw keeps the whole payload so a configured role claim can be read from
	// it without this struct having to know every provider's naming.
	raw map[string]any
}

// Exchange trades an authorisation code for a validated set of claims.
func (p *OIDCProvider) Exchange(ctx context.Context, code string, req AuthRequest) (Claims, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.config.RedirectURL)
	form.Set("client_id", p.config.ClientID)
	form.Set("code_verifier", req.Verifier)
	if p.config.ClientSecret != "" {
		form.Set("client_secret", p.config.ClientSecret)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Claims{}, fmt.Errorf("OIDC token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The provider's error body can contain the client secret in some
		// misconfigurations, so it is not included in the returned error.
		return Claims{}, fmt.Errorf("OIDC token exchange returned %s", resp.Status)
	}

	var token struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return Claims{}, fmt.Errorf("parse OIDC token response: %w", err)
	}
	if token.IDToken == "" {
		return Claims{}, errors.New("OIDC token response contained no id_token")
	}

	return p.validateIDToken(ctx, token.IDToken, req.Nonce)
}

// validateIDToken checks the signature and every claim that matters.
//
// The checks are the whole point of this file. Skipping any one of them turns
// the token from a proof of identity into an unverified assertion by whoever
// sent it.
func (p *OIDCProvider) validateIDToken(ctx context.Context, idToken, expectedNonce string) (Claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed ID token")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, errors.New("malformed ID token header")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return Claims{}, errors.New("malformed ID token header")
	}

	// Only RS256 is accepted. Taking the algorithm from the token itself is the
	// classic JWT vulnerability: "none" would skip verification entirely, and an
	// HMAC algorithm would let an attacker sign with the public key as the
	// secret. The allow-list is what closes both.
	if header.Algorithm != "RS256" {
		return Claims{}, fmt.Errorf("unsupported ID token algorithm %q: only RS256 is accepted",
			header.Algorithm)
	}

	key, err := p.signingKey(ctx, header.KeyID)
	if err != nil {
		return Claims{}, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("malformed ID token signature")
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, signed[:], signature); err != nil {
		return Claims{}, errors.New("ID token signature verification failed")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errors.New("malformed ID token payload")
	}
	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return Claims{}, errors.New("malformed ID token payload")
	}
	_ = json.Unmarshal(payloadJSON, &claims.raw)

	// The issuer must be the provider we configured, or a signature from any
	// provider we happen to trust would be accepted for any account.
	if claims.Issuer != p.discovery.Issuer {
		return Claims{}, fmt.Errorf("ID token issuer %q does not match the configured provider",
			claims.Issuer)
	}
	// The audience must be us. A token minted for a different client of the same
	// provider is not a statement about a login here.
	if !audienceContains(claims.Audience, p.config.ClientID) {
		return Claims{}, errors.New("ID token audience does not include this client")
	}

	now := time.Now()
	// A small skew allowance: clocks between two servers are never identical,
	// and rejecting a token that expired two seconds ago by our clock produces
	// mystifying intermittent login failures.
	const skew = 2 * time.Minute
	if claims.Expiry == 0 || now.After(time.Unix(claims.Expiry, 0).Add(skew)) {
		return Claims{}, errors.New("ID token has expired")
	}
	if claims.IssuedAt != 0 && now.Add(skew).Before(time.Unix(claims.IssuedAt, 0)) {
		return Claims{}, errors.New("ID token was issued in the future")
	}
	// The nonce ties this token to the login this browser started. Without the
	// check, a token obtained in another context could be replayed here.
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return Claims{}, errors.New("ID token nonce does not match the login request")
	}
	if claims.Subject == "" {
		return Claims{}, errors.New("ID token contains no subject claim")
	}
	return claims, nil
}

// audienceContains handles the audience claim being either a string or a list,
// which the specification permits and providers differ on.
func audienceContains(audience any, clientID string) bool {
	switch value := audience.(type) {
	case string:
		return value == clientID
	case []any:
		for _, item := range value {
			if s, ok := item.(string); ok && s == clientID {
				return true
			}
		}
	}
	return false
}

// signingKey returns the provider's public key for a key id, refreshing the
// cache when the key is unknown or stale.
func (p *OIDCProvider) signingKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	p.mu.RLock()
	key, ok := p.keys[keyID]
	fresh := time.Since(p.keysFetched) < jwksCacheTTL
	p.mu.RUnlock()

	if ok && fresh {
		return key, nil
	}

	if err := p.refreshKeys(ctx); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	if key, ok := p.keys[keyID]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("no signing key %q published by the provider", keyID)
}

// refreshKeys fetches the provider's JWKS.
func (p *OIDCProvider) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.discovery.JWKSURI, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch OIDC signing keys: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OIDC JWKS endpoint returned %s", resp.Status)
	}

	var jwks struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			KeyID     string `json:"kid"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jwks); err != nil {
		return fmt.Errorf("parse OIDC JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.KeyType != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(k.Modulus)
		if err != nil {
			continue
		}
		exponent, err := base64.RawURLEncoding.DecodeString(k.Exponent)
		if err != nil {
			continue
		}
		// The exponent is a big-endian byte string, almost always 65537.
		e := 0
		for _, b := range exponent {
			e = e<<8 | int(b)
		}
		if e == 0 {
			continue
		}
		keys[k.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	if len(keys) == 0 {
		return errors.New("OIDC provider published no usable RSA signing keys")
	}

	p.mu.Lock()
	p.keys = keys
	p.keysFetched = time.Now()
	p.mu.Unlock()
	return nil
}

// MappedRole resolves the application role for a set of claims.
//
// When no mapping is configured, or the claim yields nothing recognised, the
// caller gets an empty string and is expected to fall back to the least
// privileged role. Defaulting to anything else would let a directory change
// grant access nobody intended.
func (p *OIDCProvider) MappedRole(claims Claims) string {
	if p.config.RoleClaim == "" || len(p.config.RoleMapping) == 0 {
		return ""
	}
	value, ok := claims.raw[p.config.RoleClaim]
	if !ok {
		return ""
	}

	// The claim may be a single value or a list of group names.
	switch typed := value.(type) {
	case string:
		return p.config.RoleMapping[typed]
	case []any:
		// Where a user is in several mapped groups, the most privileged wins -
		// the alternative, first-match, would depend on the provider's ordering.
		best := ""
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				continue
			}
			if role, ok := p.config.RoleMapping[name]; ok && rolePrivilege(role) > rolePrivilege(best) {
				best = role
			}
		}
		return best
	}
	return ""
}

// rolePrivilege ranks roles so that a multi-group user gets a deterministic
// result.
func rolePrivilege(role string) int {
	switch role {
	case "admin":
		return 4
	case "manager":
		return 3
	case "member":
		return 2
	case "client":
		return 1
	default:
		return 0
	}
}
