package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/service"
)

// The middleware chain.
//
// Everything here runs on every request, before any handler, which makes it the
// place where a mistake is both invisible and universal. Three of these are
// security properties rather than conveniences:
//
//   - which address is recorded in the audit trail. An unchecked forwarded
//     header lets anybody write whatever they like into it, which is worse than
//     recording no address at all.
//   - which routes may be reached with no identity. The list is deliberately
//     explicit, and the test walks the actual route table rather than the list,
//     so a new route that quietly became public fails here.
//   - whether a panic can take a response's headers with it.

// TestTheAuditTrailRecordsAnAddressNobodyCanForge.
//
// X-Forwarded-For is believed only when the machine that connected is one the
// operator named. Believing it from anybody turns the audit trail's address
// column into a free-text field written by whoever is being audited.
func TestTheAuditTrailRecordsAnAddressNobodyCanForge(t *testing.T) {
	server := &Server{cfg: config.Config{TrustedProxies: []string{"10.0.0.1", "10.0.0.2"}}}

	for _, situation := range []struct {
		name      string
		remote    string
		forwarded string
		want      string
	}{
		{
			name:      "a direct connection claiming to be somebody else",
			remote:    "203.0.113.9:51234",
			forwarded: "198.51.100.1",
			want:      "203.0.113.9",
		},
		{
			name:      "a trusted proxy reporting its client",
			remote:    "10.0.0.1:443",
			forwarded: "198.51.100.1",
			want:      "198.51.100.1",
		},
		{
			name:      "a chain through a trusted proxy: the original client is left-most",
			remote:    "10.0.0.2:443",
			forwarded: "198.51.100.1, 10.0.0.9, 10.0.0.2",
			want:      "198.51.100.1",
		},
		{
			name:      "spacing, which every proxy does differently",
			remote:    "10.0.0.1:443",
			forwarded: "  198.51.100.1 ,10.0.0.9",
			want:      "198.51.100.1",
		},
		{
			name:   "a trusted proxy that forwarded no header",
			remote: "10.0.0.1:443",
			want:   "10.0.0.1",
		},
		{
			name:      "an untrusted proxy, header and all",
			remote:    "192.0.2.50:443",
			forwarded: "198.51.100.1",
			want:      "192.0.2.50",
		},
		{
			name:   "a remote address with no port, which a unix socket produces",
			remote: "10.0.0.1",
			want:   "10.0.0.1",
		},
	} {
		t.Run(situation.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/today", nil)
			request.RemoteAddr = situation.remote
			if situation.forwarded != "" {
				request.Header.Set("X-Forwarded-For", situation.forwarded)
			}
			if got := server.clientIP(request); got != situation.want {
				t.Errorf("clientIP = %q, want %q", got, situation.want)
			}
		})
	}
}

// TestNobodyIsTrustedByDefault.
//
// The empty list means believe nobody, and it is the default. A configuration
// mistake should lose the real address, not accept a forged one.
func TestNobodyIsTrustedByDefault(t *testing.T) {
	server := &Server{cfg: config.Config{}}

	request := httptest.NewRequest(http.MethodGet, "/today", nil)
	request.RemoteAddr = "203.0.113.9:51234"
	request.Header.Set("X-Forwarded-For", "198.51.100.1")

	if got := server.clientIP(request); got != "203.0.113.9" {
		t.Errorf("clientIP = %q with no trusted proxies configured", got)
	}
}

// TestForwardedProtocolIsBelievedOnTheSameTerms.
//
// It decides whether HSTS is sent and whether a cookie is marked Secure. A
// forged header would make the application believe a plain-HTTP request arrived
// over TLS - which is only a real risk if anybody may forge it, hence the same
// trusted-proxy gate as the address.
func TestForwardedProtocolIsBelievedOnTheSameTerms(t *testing.T) {
	server := &Server{cfg: config.Config{TrustedProxies: []string{"10.0.0.1"}}}

	untrusted := httptest.NewRequest(http.MethodGet, "/today", nil)
	untrusted.RemoteAddr = "203.0.113.9:51234"
	untrusted.Header.Set("X-Forwarded-Proto", "https")
	if server.isHTTPS(untrusted) {
		t.Error("an untrusted peer convinced the server it was speaking TLS")
	}

	trusted := httptest.NewRequest(http.MethodGet, "/today", nil)
	trusted.RemoteAddr = "10.0.0.1:443"
	trusted.Header.Set("X-Forwarded-Proto", "https")
	if !server.isHTTPS(trusted) {
		t.Error("a trusted proxy's X-Forwarded-Proto was ignored")
	}

	// Case, because proxies differ and the header is not case-sensitive.
	trusted.Header.Set("X-Forwarded-Proto", "HTTPS")
	if !server.isHTTPS(trusted) {
		t.Error("X-Forwarded-Proto: HTTPS was not recognised")
	}
	trusted.Header.Set("X-Forwarded-Proto", "http")
	if server.isHTTPS(trusted) {
		t.Error("a proxy reporting plain HTTP was read as TLS")
	}
}

// TestHSTSIsSentOnlyWhenItIsBothMeaningfulAndAskedFor.
//
// Enabling it by default is hostile: a browser that has seen the header refuses
// plain HTTP to that host for the whole max-age, and there is no way to clear it
// remotely. Sending it over plain HTTP is merely meaningless.
func TestHSTSIsSentOnlyWhenItIsBothMeaningfulAndAskedFor(t *testing.T) {
	for _, situation := range []struct {
		name    string
		maxAge  int
		proto   string
		wantSet bool
	}{
		{"not configured, over plain HTTP", 0, "http", false},
		{"not configured, over TLS", 0, "https", false},
		{"configured, over plain HTTP", 15552000, "http", false},
		{"configured, over TLS", 15552000, "https", true},
	} {
		t.Run(situation.name, func(t *testing.T) {
			server := &Server{cfg: config.Config{
				HSTSMaxAgeSeconds: situation.maxAge,
				TrustedProxies:    []string{"10.0.0.1"},
			}}
			handler := server.withSecurityHeaders(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

			request := httptest.NewRequest(http.MethodGet, "/today", nil)
			request.RemoteAddr = "10.0.0.1:443"
			request.Header.Set("X-Forwarded-Proto", situation.proto)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			header := recorder.Header().Get("Strict-Transport-Security")
			if situation.wantSet && header == "" {
				t.Error("HSTS was asked for over TLS and not sent")
			}
			if !situation.wantSet && header != "" {
				t.Errorf("HSTS was sent as %q", header)
			}
			if situation.wantSet && !strings.Contains(header, "15552000") {
				t.Errorf("HSTS = %q, want the configured max-age", header)
			}
		})
	}
}

// TestEveryRouteRequiresAnIdentity.
//
// The unauthenticated surface is four routes and a health check, and the list
// that names them is explicit precisely so nothing joins it by accident. That
// argument only holds if somebody checks - so this walks the route table itself
// and asks each route what an anonymous caller gets.
//
// Anything other than the login page is a failure, including a 500: a route that
// panics on a nil user has still been reached without one.
func TestEveryRouteRequiresAnIdentity(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	public := map[string]bool{
		"/login": true, "/logout": true, "/healthz": true,
		"/auth/oidc/start": true, "/auth/oidc/callback": true,
		// Embedded bytes with nothing in them; see TestTheRootIconsAreServedWithoutALogin.
		"/favicon.ico": true, "/apple-touch-icon.png": true,
		"/apple-touch-icon-precomposed.png": true, "/site.webmanifest": true,
	}

	routes := declaredRoutes(t)
	if len(routes) < 40 {
		t.Fatalf("only found %d routes; the scan is not reading routes.go", len(routes))
	}

	for _, route := range routes {
		if public[route.path] || strings.HasPrefix(route.path, "/static/") {
			continue
		}
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.concrete(), nil)
			if route.method == http.MethodPost {
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			recorder := httptest.NewRecorder()
			srv.ServeHTTP(recorder, request)

			switch recorder.Code {
			case http.StatusSeeOther, http.StatusFound:
				if location := recorder.Header().Get("Location"); !strings.HasPrefix(location, "/login") {
					t.Errorf("an anonymous request was redirected to %q rather than "+
						"to the login page", location)
				}
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
				// Also fine: refused without a hint about what is there.
			default:
				t.Errorf("an anonymous %s %s got %d; every route but the login "+
					"surface requires an identity (ASR-005)",
					route.method, route.concrete(), recorder.Code)
			}
		})
	}
}

// TestTheRootIconsAreServedWithoutALogin.
//
// A browser asks for /favicon.ico before anybody signs in - including on the
// login page, which is the one page an anonymous visitor is meant to see. These
// aliases exist so that a request a browser makes anyway does not become a 404
// in the log; until this test they became a 303 to the login page instead, so
// the login screen had no icon and every anonymous probe still cost a line.
//
// They are embedded bytes with nothing in them, which is what makes exposing
// them a decision rather than a risk.
func TestTheRootIconsAreServedWithoutALogin(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	for _, path := range []string{"/favicon.ico", "/apple-touch-icon.png", "/site.webmanifest"} {
		recorder := httptest.NewRecorder()
		srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d for an anonymous browser", path, recorder.Code)
		}
	}
}

// TestAPanicBecomesAFiveHundredAndNothingElse.
//
// One handler dereferencing a nil pointer must not take the process with it, and
// the stack trace must not reach the user: it names internal paths and package
// structure, which is reconnaissance for free.
func TestAPanicBecomesAFiveHundredAndNothingElse(t *testing.T) {
	server := &Server{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	handler := server.withRecovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("a handler exploded")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/today", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("a panicking handler produced %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, leak := range []string{"a handler exploded", "goroutine", "middleware.go", "runtime."} {
		if strings.Contains(body, leak) {
			t.Errorf("the response body carries %q from the panic:\n%s", leak, body)
		}
	}
}

// TestEveryResponseCarriesARequestID.
//
// It correlates a log line with an audit row, which is the only way to answer
// "what else did that request do". Random rather than sequential so that ids
// stay unique across restarts and a search over a week cannot collide.
func TestEveryResponseCarriesARequestID(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		recorder := httptest.NewRecorder()
		srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		id := recorder.Header().Get("X-Request-Id")
		if id == "" {
			t.Fatal("a response carries no request id")
		}
		if seen[id] {
			t.Fatalf("request id %q was issued twice", id)
		}
		seen[id] = true
	}
}

// TestTheCSRFTokenBelongsToOneSession.
//
// A token is only a defence if it cannot be obtained elsewhere. One signed-in
// user's token must not authorise a request carrying another user's session
// cookie - which is what would happen if the token were derived from anything
// but the session.
func TestTheCSRFTokenBelongsToOneSession(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)

	first := signIn(t, srv)
	firstToken := csrfTokenFor(t, srv, first)

	second, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	secondCookie := &http.Cookie{Name: auth.SessionCookieName, Value: second.CookieValue}

	// The same person, signed in twice: two sessions, two tokens.
	secondToken := csrfTokenFor(t, srv, secondCookie)
	if firstToken == secondToken {
		t.Fatal("two sessions share a CSRF token")
	}

	// The first session's token, presented with the second session's cookie.
	forged := &client{srv: srv, cookie: secondCookie, token: firstToken}
	rec := forged.post(t, "/customers", url.Values{
		"name": {"Forged"}, "currency": {"SEK"},
	})
	if rec.Code < 400 {
		t.Errorf("a token from another session was accepted: %d", rec.Code)
	}
}

// route is one entry from the route table.
type route struct {
	method string
	path   string
}

// concrete substitutes a plausible id for each wildcard, so the request reaches
// the handler rather than the mux's own 404.
func (r route) concrete() string {
	return regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(r.path, "1")
}

// declaredRoutes reads the route table out of routes.go.
//
// Reading the source rather than asking the mux, because Go's ServeMux does not
// enumerate its patterns - and because the table is one function on purpose, so
// that it can be read this way.
func declaredRoutes(t *testing.T) []route {
	t.Helper()

	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}

	pattern := regexp.MustCompile(`mux\.(?:HandleFunc|Handle)\("(GET|POST|PUT|DELETE) ([^"]+)"`)
	var routes []route
	for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
		path := match[2]
		// {$} is the mux's "exactly the root" marker rather than a wildcard.
		if path == "/{$}" {
			path = "/"
		}
		routes = append(routes, route{method: match[1], path: path})
	}
	return routes
}
