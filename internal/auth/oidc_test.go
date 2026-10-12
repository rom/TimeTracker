package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The OIDC flow, against a provider this test controls.
//
// Every check in validateIDToken is a rule that, skipped, turns the ID token
// from a proof of identity into an assertion by whoever sent it. None of them
// had a test. They are the kind of code that looks obviously right and is
// obviously right until somebody simplifies it - and where the consequence of a
// missing line is not a wrong number on a screen but somebody else's login.
//
// So the shape here is: stand up a provider, mint tokens with it, and then mint
// the tokens an attacker would. A test that only proves the happy path works
// proves nothing about a validator, because a validator that accepts everything
// passes it.

// fakeProvider is an OIDC provider: discovery, JWKS, and a token endpoint that
// hands back whatever the test told it to.
type fakeProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
	// second is a key the provider has *not* published, for the forged-signature
	// cases.
	second *rsa.PrivateKey
	// idToken is what the token endpoint returns; tests set it per case.
	idToken string
	// tokenStatus lets a test make the exchange fail.
	tokenStatus int
	// tokenBody overrides the whole token response.
	tokenBody string
	// lastTokenForm records what was posted to the token endpoint.
	lastTokenForm url.Values
	// issuerOverride makes the discovery document disagree with its URL.
	issuerOverride string
	// jwksRequests counts key fetches, so cache behaviour is observable.
	jwksRequests int
	// publishKey decides which key is in the JWKS.
	publishKey func() *rsa.PrivateKey
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()

	first, second := testKeys(t)
	provider := &fakeProvider{key: first, second: second, keyID: "key-1", tokenStatus: http.StatusOK}
	provider.publishKey = func() *rsa.PrivateKey { return provider.key }

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := provider.server.URL
		if provider.issuerOverride != "" {
			issuer = provider.issuerOverride
		}
		writeJSON(w, map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": provider.server.URL + "/authorize",
			"token_endpoint":         provider.server.URL + "/token",
			"jwks_uri":               provider.server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		provider.jwksRequests++
		published := provider.publishKey()
		writeJSON(w, map[string]any{"keys": []any{map[string]string{
			"kty": "RSA",
			"kid": provider.keyID,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(published.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(published.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		provider.lastTokenForm = r.PostForm
		if provider.tokenStatus != http.StatusOK {
			w.WriteHeader(provider.tokenStatus)
			// A misconfigured provider can echo the client secret back in an
			// error body; the test asserts it does not reach the caller.
			_, _ = w.Write([]byte(`{"error":"invalid_client","secret":"s3cret-do-not-leak"}`))
			return
		}
		if provider.tokenBody != "" {
			_, _ = w.Write([]byte(provider.tokenBody))
			return
		}
		writeJSON(w, map[string]string{"id_token": provider.idToken})
	})

	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

// testKeys returns the two RSA keys every case in this file uses.
//
// Generated once for the package rather than per test. 2048 bits is the
// smallest a real provider would use and costs tens of milliseconds; the table
// below has a dozen cases, and paying for it in each of them turns a fast file
// into a slow one for no extra coverage. The keys are only ever used to sign
// tokens this test then feeds back to itself.
var (
	keysOnce    sync.Once
	testKeyA    *rsa.PrivateKey
	testKeyB    *rsa.PrivateKey
	testKeysErr error
)

func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()

	keysOnce.Do(func() {
		testKeyA, testKeysErr = rsa.GenerateKey(rand.Reader, 2048)
		if testKeysErr != nil {
			return
		}
		testKeyB, testKeysErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if testKeysErr != nil {
		t.Fatalf("generate the test signing keys: %v", testKeysErr)
	}
	return testKeyA, testKeyB
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// connect discovers the provider and returns it configured for this test.
func (f *fakeProvider) connect(t *testing.T, adjust ...func(*OIDCConfig)) *OIDCProvider {
	t.Helper()

	cfg := OIDCConfig{
		Issuer:      f.server.URL,
		ClientID:    "timetracker",
		RedirectURL: "https://tt.example.com/auth/callback",
	}
	for _, fn := range adjust {
		fn(&cfg)
	}
	provider, err := NewOIDCProvider(context.Background(), cfg, f.server.Client())
	if err != nil {
		t.Fatalf("discover the provider: %v", err)
	}
	return provider
}

// token mints an ID token. The claims and the signing arrangements are both
// under the test's control, which is what lets the attack cases be written.
func (f *fakeProvider) token(t *testing.T, claims map[string]any, adjust ...func(header map[string]any)) string {
	t.Helper()

	header := map[string]any{"alg": "RS256", "kid": f.keyID, "typ": "JWT"}
	for _, fn := range adjust {
		fn(header)
	}
	return signJWT(t, header, claims, f.key)
}

// signJWT encodes and signs, or leaves the signature empty for "alg":"none".
func signJWT(t *testing.T, header map[string]any, claims map[string]any, key *rsa.PrivateKey) string {
	t.Helper()

	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signingInput := encode(header) + "." + encode(claims)

	if header["alg"] == "none" {
		return signingInput + "."
	}
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// goodClaims is a token that should be accepted, which each attack case then
// spoils in exactly one way.
func (f *fakeProvider) goodClaims(nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":            f.server.URL,
		"aud":            "timetracker",
		"sub":            "user-42",
		"email":          "someone@example.com",
		"email_verified": true,
		"name":           "Someone",
		"nonce":          nonce,
		"iat":            now.Add(-time.Minute).Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
}

// exchange runs one login against the provider and returns what came back.
func exchange(t *testing.T, provider *OIDCProvider, fake *fakeProvider, claims map[string]any, nonce string, adjust ...func(map[string]any)) (Claims, error) {
	t.Helper()

	fake.idToken = fake.token(t, claims, adjust...)
	return provider.Exchange(context.Background(), "an-authorisation-code",
		AuthRequest{Nonce: nonce, Verifier: "a-verifier", State: "a-state"})
}

// TestOIDCLoginSucceedsAndCarriesTheClaims.
//
// The happy path, first, because every case below is this one with a single
// thing wrong. If this were subtly broken the failures below would all pass for
// the wrong reason.
func TestOIDCLoginSucceedsAndCarriesTheClaims(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	claims, err := exchange(t, provider, fake, fake.goodClaims("nonce-1"), "nonce-1")
	if err != nil {
		t.Fatalf("a valid login was refused: %v", err)
	}
	if claims.Subject != "user-42" {
		t.Errorf("subject = %q, want user-42", claims.Subject)
	}
	if claims.Email != "someone@example.com" || !claims.EmailVerified {
		t.Errorf("email claims lost: %+v", claims)
	}
	if claims.Name != "Someone" {
		t.Errorf("name = %q", claims.Name)
	}

	// The token request has to carry the PKCE verifier, or an intercepted code
	// would be usable by whoever intercepted it.
	if got := fake.lastTokenForm.Get("code_verifier"); got != "a-verifier" {
		t.Errorf("the token exchange sent code_verifier=%q", got)
	}
	if got := fake.lastTokenForm.Get("grant_type"); got != "authorization_code" {
		t.Errorf("grant_type = %q", got)
	}
}

// TestOIDCRejectsTokensItShould.
//
// One table, one spoiled claim each. Written as a table because the argument for
// each rule is the same shape - "without this check, X is accepted" - and
// because a rule that is quietly deleted should fail here by name.
func TestOIDCRejectsTokensItShould(t *testing.T) {
	for _, attack := range []struct {
		name   string
		spoil  func(claims map[string]any)
		header func(header map[string]any)
		want   string
	}{
		{
			name:  "expired an hour ago",
			spoil: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
			want:  "expired",
		},
		{
			name:  "no expiry at all",
			spoil: func(c map[string]any) { delete(c, "exp") },
			want:  "expired",
		},
		{
			name:  "issued an hour in the future",
			spoil: func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() },
			want:  "future",
		},
		{
			name:  "minted by a different issuer",
			spoil: func(c map[string]any) { c["iss"] = "https://evil.example.com" },
			want:  "issuer",
		},
		{
			name:  "minted for a different client of the same provider",
			spoil: func(c map[string]any) { c["aud"] = "some-other-app" },
			want:  "audience",
		},
		{
			name:  "audience list without us in it",
			spoil: func(c map[string]any) { c["aud"] = []any{"app-a", "app-b"} },
			want:  "audience",
		},
		{
			name:  "replayed from another login",
			spoil: func(c map[string]any) { c["nonce"] = "a-nonce-from-somewhere-else" },
			want:  "nonce",
		},
		{
			name:  "no subject to identify anybody by",
			spoil: func(c map[string]any) { delete(c, "sub") },
			want:  "subject",
		},
		{
			name:   "unsigned, with alg none",
			spoil:  func(c map[string]any) {},
			header: func(h map[string]any) { h["alg"] = "none" },
			want:   "RS256",
		},
		{
			name:   "signed with a symmetric algorithm",
			spoil:  func(c map[string]any) {},
			header: func(h map[string]any) { h["alg"] = "HS256" },
			want:   "RS256",
		},
		{
			name:   "signed by a key the provider never published",
			spoil:  func(c map[string]any) {},
			header: func(h map[string]any) { h["kid"] = "some-other-key" },
			want:   "signing key",
		},
	} {
		t.Run(attack.name, func(t *testing.T) {
			fake := newFakeProvider(t)
			provider := fake.connect(t)

			claims := fake.goodClaims("nonce-1")
			attack.spoil(claims)

			var adjust []func(map[string]any)
			if attack.header != nil {
				adjust = append(adjust, attack.header)
			}
			_, err := exchange(t, provider, fake, claims, "nonce-1", adjust...)
			if err == nil {
				t.Fatalf("a token that was %s was accepted", attack.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(attack.want)) {
				t.Errorf("refused for the wrong reason: %v (wanted something about %q)",
					err, attack.want)
			}
		})
	}
}

// TestOIDCRejectsAForgedSignature.
//
// The case the table cannot express: a well-formed RS256 token, with the key id
// of a key the provider really did publish, signed by a different key. This is
// what an attacker who can mint tokens produces, and the only thing that catches
// it is the signature check itself.
func TestOIDCRejectsAForgedSignature(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	forged := signJWT(t,
		map[string]any{"alg": "RS256", "kid": fake.keyID, "typ": "JWT"},
		fake.goodClaims("nonce-1"),
		fake.second)
	fake.idToken = forged

	if _, err := provider.Exchange(context.Background(), "code",
		AuthRequest{Nonce: "nonce-1", Verifier: "v"}); err == nil {
		t.Fatal("a token signed with an unpublished key was accepted")
	} else if !strings.Contains(err.Error(), "signature") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// TestOIDCAcceptsAnAudienceList.
//
// The specification allows the audience to be a list, and providers differ. A
// validator that only handled the string form would refuse every login against
// half of them - which is a failure that looks like a configuration problem and
// gets "fixed" by removing the check.
func TestOIDCAcceptsAnAudienceList(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	claims := fake.goodClaims("nonce-1")
	claims["aud"] = []any{"another-app", "timetracker"}

	if _, err := exchange(t, provider, fake, claims, "nonce-1"); err != nil {
		t.Errorf("a token whose audience list includes us was refused: %v", err)
	}
}

// TestOIDCToleratesASmallClockSkew.
//
// Two servers never agree on the time to the second, and refusing a token that
// expired a moment ago by our clock produces intermittent login failures nobody
// can reproduce. The allowance is two minutes, so a token that expired one
// minute ago is still good and one that expired an hour ago is not - the latter
// is the case above, which is what stops this from being an excuse.
func TestOIDCToleratesASmallClockSkew(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	claims := fake.goodClaims("nonce-1")
	claims["exp"] = time.Now().Add(-time.Minute).Unix()

	if _, err := exchange(t, provider, fake, claims, "nonce-1"); err != nil {
		t.Errorf("a token one minute past expiry was refused: %v", err)
	}
}

// TestOIDCRefetchesKeysAfterRotation.
//
// Providers rotate signing keys on their own schedule, without telling anybody.
// A token signed with a key we have not seen is the normal signal that it
// happened - so it has to trigger a refetch rather than a refusal, or every
// login fails until the process is restarted.
func TestOIDCRefetchesKeysAfterRotation(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	if _, err := exchange(t, provider, fake, fake.goodClaims("n1"), "n1"); err != nil {
		t.Fatalf("first login: %v", err)
	}
	fetchesAfterFirst := fake.jwksRequests

	// A second login uses the cache rather than fetching again.
	if _, err := exchange(t, provider, fake, fake.goodClaims("n2"), "n2"); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if fake.jwksRequests != fetchesAfterFirst {
		t.Errorf("the key cache is not being used: %d fetches for two logins",
			fake.jwksRequests)
	}

	// The provider rotates: same key id, new key material.
	fake.key = fake.second
	if _, err := exchange(t, provider, fake, fake.goodClaims("n3"), "n3"); err == nil {
		t.Error("a token signed with the rotated key was accepted before the " +
			"cached key expired; the cache is not being consulted at all")
	}

	// A new key id is the signal that keys moved, and must be followed.
	fake.keyID = "key-2"
	if _, err := exchange(t, provider, fake, fake.goodClaims("n4"), "n4"); err != nil {
		t.Errorf("a login after key rotation failed: %v", err)
	}
	if fake.jwksRequests <= fetchesAfterFirst {
		t.Error("rotation did not trigger a key refetch")
	}
}

// TestOIDCDiscoveryRefusesAMismatchedIssuer.
//
// A redirect during discovery could point us at another provider's metadata
// while we believe we are talking to the configured one. The issuer inside the
// document is what settles it.
func TestOIDCDiscoveryRefusesAMismatchedIssuer(t *testing.T) {
	fake := newFakeProvider(t)
	fake.issuerOverride = "https://someone-elses-directory.example.com"

	_, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Issuer: fake.server.URL, ClientID: "timetracker",
		RedirectURL: "https://tt.example.com/auth/callback",
	}, fake.server.Client())
	if err == nil {
		t.Fatal("discovery accepted a document claiming a different issuer")
	}
	if !strings.Contains(err.Error(), "issuer mismatch") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// TestOIDCDiscoveryNeedsItsConfiguration.
//
// Each of these is a half-finished configuration, and the operator should be
// told at startup rather than at the moment somebody tries to sign in.
func TestOIDCDiscoveryNeedsItsConfiguration(t *testing.T) {
	fake := newFakeProvider(t)

	for _, missing := range []struct {
		name string
		cfg  OIDCConfig
	}{
		{"no issuer", OIDCConfig{ClientID: "a", RedirectURL: "https://x/cb"}},
		{"no client id", OIDCConfig{Issuer: fake.server.URL, RedirectURL: "https://x/cb"}},
		{"no redirect URL", OIDCConfig{Issuer: fake.server.URL, ClientID: "a"}},
	} {
		if _, err := NewOIDCProvider(context.Background(), missing.cfg, fake.server.Client()); err == nil {
			t.Errorf("%s was accepted", missing.name)
		}
	}
}

// TestOIDCDefaultsToTheStandardScopes.
//
// openid is required by the protocol; email and profile are what give a new
// account a name and an address to show. An operator who configures nothing
// should still get a working login with a display name.
func TestOIDCDefaultsToTheStandardScopes(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	request, err := NewAuthRequest()
	if err != nil {
		t.Fatalf("new auth request: %v", err)
	}
	target, err := url.Parse(provider.AuthorizationURL(request))
	if err != nil {
		t.Fatalf("the authorisation URL does not parse: %v", err)
	}
	scopes := strings.Fields(target.Query().Get("scope"))
	for _, wanted := range []string{"openid", "email", "profile"} {
		if !containsString(scopes, wanted) {
			t.Errorf("the default scopes do not include %s: %v", wanted, scopes)
		}
	}
}

// TestAuthorizationURLUsesPKCEProperly.
//
// The challenge must be the SHA-256 of the verifier and must say so, and the
// verifier itself must never appear in a URL the browser follows - which is the
// entire difference between PKCE and a value sent twice.
func TestAuthorizationURLUsesPKCEProperly(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	request, err := NewAuthRequest()
	if err != nil {
		t.Fatalf("new auth request: %v", err)
	}
	raw := provider.AuthorizationURL(request)
	target, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the authorisation URL does not parse: %v", err)
	}
	query := target.Query()

	digest := sha256.Sum256([]byte(request.Verifier))
	if got, want := query.Get("code_challenge"), base64.RawURLEncoding.EncodeToString(digest[:]); got != want {
		t.Errorf("code_challenge = %q, want the S256 hash of the verifier", got)
	}
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256: plain sends the verifier "+
			"through the browser, which is the thing PKCE exists to avoid", got)
	}
	if strings.Contains(raw, request.Verifier) {
		t.Error("the PKCE verifier is in the authorisation URL")
	}
	for _, required := range []string{"state", "nonce", "client_id", "redirect_uri"} {
		if query.Get(required) == "" {
			t.Errorf("the authorisation URL has no %s", required)
		}
	}
	if query.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", query.Get("response_type"))
	}
}

// TestAuthRequestValuesAreUnpredictable.
//
// State, nonce and verifier are all defences that work by being unguessable.
// Two requests sharing any of them would mean the generator is not what it
// appears to be.
func TestAuthRequestValuesAreUnpredictable(t *testing.T) {
	seen := map[string]string{}
	for i := 0; i < 50; i++ {
		request, err := NewAuthRequest()
		if err != nil {
			t.Fatalf("new auth request: %v", err)
		}
		for name, value := range map[string]string{
			"state": request.State, "nonce": request.Nonce, "verifier": request.Verifier,
		} {
			if value == request.State && name != "state" {
				t.Fatalf("%s is the same value as the state", name)
			}
			if raw, err := base64.RawURLEncoding.DecodeString(value); err != nil || len(raw) < 32 {
				t.Fatalf("%s is %d bytes of entropy (err %v); want at least 32",
					name, len(raw), err)
			}
			if where, repeated := seen[value]; repeated {
				t.Fatalf("%s repeated a value first seen as %s", name, where)
			}
			seen[value] = name
		}
	}
}

// TestFailedExchangeDoesNotLeakTheProviderResponse.
//
// A misconfigured provider can put the client secret in its error body. The
// operator needs to know the exchange failed and with what status; they do not
// need it in a log line that gets pasted into a ticket.
func TestFailedExchangeDoesNotLeakTheProviderResponse(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)
	fake.tokenStatus = http.StatusUnauthorized

	_, err := provider.Exchange(context.Background(), "code", AuthRequest{Verifier: "v"})
	if err == nil {
		t.Fatal("a 401 from the token endpoint was treated as a login")
	}
	if strings.Contains(err.Error(), "s3cret-do-not-leak") {
		t.Errorf("the provider's error body reached the caller: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// TestATokenResponseWithoutAnIDTokenIsRefused.
//
// An OAuth2 provider that is not an OIDC provider returns an access token and no
// id_token. There is no identity in that response, and treating its absence as
// anything but a failure would be treating "no answer" as "yes".
func TestATokenResponseWithoutAnIDTokenIsRefused(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)
	fake.tokenBody = `{"access_token":"at","token_type":"Bearer"}`

	if _, err := provider.Exchange(context.Background(), "code", AuthRequest{Verifier: "v"}); err == nil {
		t.Fatal("a token response with no id_token was accepted")
	}
}

// TestMalformedTokensAreRefusedRatherThanPanicking.
//
// These arrive from the network. Every one of them is a shape somebody will send
// eventually, deliberately or through a proxy that rewrote something, and none
// of them should reach a stack trace.
func TestMalformedTokensAreRefusedRatherThanPanicking(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t)

	for _, malformed := range []struct{ name, token string }{
		{"empty", ""},
		{"one part", "onlyonepart"},
		{"two parts", "header.payload"},
		{"four parts", "a.b.c.d"},
		{"header is not base64", "!!!.eyJ9.sig"},
		{"header is not JSON", base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".e30.sig"},
		{"payload is not base64", base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + ".!!!.sig"},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			fake.idToken = malformed.token
			if _, err := provider.Exchange(context.Background(), "code",
				AuthRequest{Verifier: "v"}); err == nil {
				t.Errorf("a %s token was accepted", malformed.name)
			}
		})
	}
}

// TestRoleMappingPrefersThePrivilegedGroup.
//
// A directory puts people in several groups, and the order it lists them in is
// its business rather than a decision about access. First-match would make
// somebody's role depend on it.
//
// Nothing recognised maps to nothing, and the caller falls back to the least
// privileged role: defaulting the other way would let a directory change grant
// access nobody intended.
func TestRoleMappingPrefersThePrivilegedGroup(t *testing.T) {
	fake := newFakeProvider(t)
	provider := fake.connect(t, func(cfg *OIDCConfig) {
		cfg.RoleClaim = "groups"
		cfg.RoleMapping = map[string]string{
			"tt-admins":   "admin",
			"tt-managers": "manager",
			"tt-staff":    "member",
			"tt-clients":  "client",
		}
	})

	for _, mapping := range []struct {
		name   string
		groups any
		want   string
	}{
		{"one group", []any{"tt-managers"}, "manager"},
		{"a plain string claim", "tt-admins", "admin"},
		{"several, admin last", []any{"tt-staff", "tt-managers", "tt-admins"}, "admin"},
		{"several, admin first", []any{"tt-admins", "tt-staff"}, "admin"},
		{"manager over member", []any{"tt-staff", "tt-managers"}, "manager"},
		{"member over client", []any{"tt-clients", "tt-staff"}, "member"},
		{"nothing recognised", []any{"some-other-team"}, ""},
		{"an empty list", []any{}, ""},
		{"not strings", []any{42, true}, ""},
		{"the claim is absent", nil, ""},
	} {
		t.Run(mapping.name, func(t *testing.T) {
			claims := fake.goodClaims("n")
			if mapping.groups != nil {
				claims["groups"] = mapping.groups
			}
			resolved, err := exchange(t, provider, fake, claims, "n")
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			if got := provider.MappedRole(resolved); got != mapping.want {
				t.Errorf("MappedRole = %q, want %q", got, mapping.want)
			}
		})
	}
}

// TestRoleMappingIsOffUnlessConfigured.
//
// An operator who has not configured a role claim gets no role from the
// directory at all, rather than a role inferred from whatever claim happens to
// look like one.
func TestRoleMappingIsOffUnlessConfigured(t *testing.T) {
	fake := newFakeProvider(t)

	unconfigured := fake.connect(t)
	claims, err := exchange(t, unconfigured, fake, fake.goodClaims("n"), "n")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if role := unconfigured.MappedRole(claims); role != "" {
		t.Errorf("an unconfigured provider mapped a role: %q", role)
	}

	// A claim named but nothing mapped is the same thing half-done, and must
	// also yield nothing rather than the claim's raw value.
	named := fake.connect(t, func(cfg *OIDCConfig) { cfg.RoleClaim = "groups" })
	withGroups := fake.goodClaims("n2")
	withGroups["groups"] = "administrators"
	claims, err = exchange(t, named, fake, withGroups, "n2")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if role := named.MappedRole(claims); role != "" {
		t.Errorf("a claim with no mapping produced the role %q", role)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}
