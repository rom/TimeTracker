package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/blob"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/service"
	"github.com/rom/timetracker/internal/store"
)

// The single-sign-on round trip, at the point where it meets a browser.
//
// The token validation is the auth package's, and has its own tests against a
// provider that mints forged tokens. What only this layer can get wrong is the
// round trip itself: the state, nonce and PKCE verifier have to survive a
// redirect to somebody else's site and come back, and every way that can fail
// has to end at the login page rather than at a session.
//
// Each refusal below is a login that must not happen. A callback that was
// accepted without the state it was started with is one an attacker can cause
// from their own browser.

// newOIDCServer wires server mode with single sign-on against a provider that
// serves discovery and nothing else.
//
// Nothing else is needed: every case here is refused before a token is
// exchanged, which is the point - these are the checks that run first.
func newOIDCServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	if testing.Short() {
		t.Skip("web tests touch the filesystem; skipped under -short")
	}

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 providerURL(r),
			"authorization_endpoint": providerURL(r) + "/authorize",
			"token_endpoint":         providerURL(r) + "/token",
			"jwks_uri":               providerURL(r) + "/jwks",
		})
	}))
	t.Cleanup(provider.Close)

	ctx := context.Background()
	oidc, err := auth.NewOIDCProvider(ctx, auth.OIDCConfig{
		Issuer:      provider.URL,
		ClientID:    "timetracker",
		RedirectURL: "https://time.example/auth/oidc/callback",
	}, provider.Client())
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}

	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "oidc.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(db, auth.RoleAuthorizer{IsProjectMember: db.IsProjectMember}, logger, time.Now)
	blobs, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	svc = svc.WithBlobs(blobs)

	accounts := service.NewAccounts(db, svc, time.Now)
	if _, err := accounts.BootstrapFirstAdmin(ctx, service.NewUserInput{
		DisplayName: "Admin", Email: "admin@example.com", Password: "a-long-enough-password",
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	srv, err := New(svc, config.Config{Mode: config.ModeServer}, logger, Options{
		Identity: func(r *http.Request) (domain.User, error) {
			cookie, err := r.Cookie(auth.SessionCookieName)
			if err != nil {
				return domain.User{}, auth.ErrUnauthenticated
			}
			user, _, err := accounts.ResolveSession(r.Context(), cookie.Value)
			return user, err
		},
		Accounts: accounts,
		OIDC:     oidc,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return srv, provider
}

// providerURL is the base the discovery document must advertise, which has to
// match the URL it was fetched from or the provider refuses it.
func providerURL(r *http.Request) string {
	return "http://" + r.Host
}

// TestStartingSingleSignOnKeepsItsSecretsOutOfTheURL.
//
// The state, nonce and PKCE verifier must survive the round trip to the
// provider, and must not do so in the URL: a query string reaches the
// provider's access log and, through a referrer, any page the provider
// renders. They go in a short-lived HttpOnly cookie instead.
func TestStartingSingleSignOnKeepsItsSecretsOutOfTheURL(t *testing.T) {
	srv, _ := newOIDCServer(t)

	rec := get(t, srv, "/auth/oidc/start")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /auth/oidc/start = %d: %s", rec.Code, rec.Body.String())
	}

	target, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("the redirect target is not a URL: %v", err)
	}
	if target.Query().Get("state") == "" {
		t.Error("the authorisation URL carries no state")
	}
	if got := target.Query().Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}

	var state *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == oidcStateCookie {
			state = cookie
		}
	}
	if state == nil {
		t.Fatal("starting single sign-on set no state cookie")
	}
	if !state.HttpOnly {
		t.Error("the state cookie is readable by script")
	}
	if state.SameSite != http.SameSiteLaxMode {
		t.Errorf("the state cookie's SameSite is %v, want Lax", state.SameSite)
	}
	if state.MaxAge <= 0 || state.MaxAge > int((15*time.Minute).Seconds()) {
		t.Errorf("the state cookie lives for %ds, want a short fixed window", state.MaxAge)
	}

	parts := strings.SplitN(state.Value, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("the state cookie holds %q, want state.nonce.verifier", state.Value)
	}
	nonce, verifier := parts[1], parts[2]
	if nonce == "" || verifier == "" {
		t.Fatalf("the state cookie is missing its nonce or verifier: %q", state.Value)
	}

	// The nonce does travel to the provider - that is its job. It is sent so
	// the provider embeds it in the ID token, and the comparison on the way
	// back is what ties that token to this login rather than to a replay.
	if target.Query().Get("nonce") != nonce {
		t.Errorf("the authorisation URL's nonce is %q, want the one in the cookie",
			target.Query().Get("nonce"))
	}

	// The PKCE verifier does not. Only its hash is sent, and finding the
	// verifier itself in the query string would mean PKCE protects nothing:
	// the secret it proves knowledge of would have travelled with the request
	// that uses it.
	if strings.Contains(rec.Header().Get("Location"), verifier) {
		t.Error("the PKCE verifier is in the authorisation URL, where the provider's " +
			"access log and any referrer header would carry it - only its hash may be sent")
	}
	if target.Query().Get("code_challenge") == "" {
		t.Error("the authorisation URL carries no code challenge")
	}
}

// TestACallbackIsRefusedUnlessThisBrowserStartedIt.
//
// Every one of these is a login that must not happen, and each ends at the
// login page with a message rather than at a session.
func TestACallbackIsRefusedUnlessThisBrowserStartedIt(t *testing.T) {
	srv, _ := newOIDCServer(t)

	// A real start, to get a state cookie that matches something.
	start := get(t, srv, "/auth/oidc/start")
	var state *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == oidcStateCookie {
			state = cookie
		}
	}
	if state == nil {
		t.Fatal("no state cookie to work with")
	}
	realState := strings.SplitN(state.Value, ".", 3)[0]

	cases := []struct {
		name   string
		query  string
		cookie *http.Cookie
		status int
	}{
		{
			name:   "no state cookie at all, which is a callback nobody started",
			query:  "?code=anything&state=" + realState,
			status: http.StatusBadRequest,
		},
		{
			name:   "a state cookie that is not three parts",
			query:  "?code=anything&state=" + realState,
			cookie: &http.Cookie{Name: oidcStateCookie, Value: "mangled"},
			status: http.StatusBadRequest,
		},
		{
			name:   "a state that does not match the one this browser started with",
			query:  "?code=anything&state=somebody-elses-state",
			cookie: state,
			status: http.StatusBadRequest,
		},
		{
			name:   "no code, so there is nothing to exchange",
			query:  "?state=" + realState,
			cookie: state,
			status: http.StatusBadRequest,
		},
		{
			name:   "the provider reporting its own failure",
			query:  "?error=access_denied&error_description=user+said+no",
			cookie: state,
			status: http.StatusUnauthorized,
		},
		{
			name:   "a code the provider will not exchange",
			query:  "?code=not-a-real-code&state=" + realState,
			cookie: state,
			status: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback"+tc.query, nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Errorf("= %d, want %d\n%s", rec.Code, tc.status, truncate(rec.Body.String()))
			}
			for _, cookie := range rec.Result().Cookies() {
				if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
					t.Error("a refused callback handed out a session")
				}
			}
			// The login page, so somebody can try again.
			if !strings.Contains(rec.Body.String(), `name="password"`) {
				t.Errorf("a refused callback does not land on the login page:\n%s",
					truncate(rec.Body.String()))
			}
		})
	}

	// The provider's own error text is not rendered back: it is
	// attacker-influenced content, and only the fact of a failure is shown.
	req := httptest.NewRequest(http.MethodGet,
		"/auth/oidc/callback?error=<script>alert(1)</script>", nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "alert(1)") {
		t.Error("the provider's error text was rendered into the page")
	}
}

// TestTheStateCookieIsSingleUse.
//
// Clearing it on the way in stops a replayed callback being processed twice -
// the same authorisation code arriving again, from a log, a history entry or a
// shared link, must not produce a second session.
func TestTheStateCookieIsSingleUse(t *testing.T) {
	srv, _ := newOIDCServer(t)

	start := get(t, srv, "/auth/oidc/start")
	var state *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == oidcStateCookie {
			state = cookie
		}
	}
	if state == nil {
		t.Fatal("no state cookie")
	}

	req := httptest.NewRequest(http.MethodGet,
		"/auth/oidc/callback?code=anything&state="+strings.SplitN(state.Value, ".", 3)[0], nil)
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == oidcStateCookie && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the callback did not clear the state cookie, so it can be replayed")
	}
}
