package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/auth"
)

// tlsState stands in for a terminated TLS connection. Only its presence
// matters: that is all the server looks at.
var tlsState = tls.ConnectionState{}

// Signing in and out, and the two places the application decides whether a
// connection is secure.

// TestEveryLoginFailureLooksTheSame.
//
// Wrong password, unknown account, disabled account, rate limited: one message
// for all of them. Distinguishing them turns the login form into an account
// enumeration tool - somebody who can learn which addresses have accounts here
// has learnt who this company works with.
func TestEveryLoginFailureLooksTheSame(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	c := newClient(t, srv)

	// A disabled account, to be refused the same way as an unknown one.
	c.post(t, "/users", url.Values{
		"display_name": {"Former Colleague"}, "email": {"former@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	})
	c.post(t, "/users/2", url.Values{
		"display_name": {"Former Colleague"}, "email": {"former@example.com"},
		"role": {"member"},
	})

	attempts := map[string]url.Values{
		"the wrong password": {
			"email": {"admin@example.com"}, "password": {"not-the-password"},
		},
		"an address with no account": {
			"email": {"nobody@example.com"}, "password": {"a-long-enough-password"},
		},
		"a disabled account, with its real password": {
			"email": {"former@example.com"}, "password": {"a-long-enough-password"},
		},
		"no password at all": {
			"email": {"admin@example.com"},
		},
		"nothing at all": {},
	}

	var bodies []string
	for name, form := range attempts {
		t.Run(name, func(t *testing.T) {
			rec := post(t, srv, "/login", form)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("= %d, want 401\n%s", rec.Code, truncate(rec.Body.String()))
			}
			for _, cookie := range rec.Result().Cookies() {
				if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
					t.Error("a failed login handed out a session")
				}
			}
			// The form comes back, so somebody can try again.
			if !strings.Contains(rec.Body.String(), `name="password"`) {
				t.Error("the login form did not come back")
			}
			bodies = append(bodies, rec.Body.String())
		})
	}

	// And they are the same page, not merely the same status.
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Error("two login failures produced different pages, which is enough " +
				"to tell which addresses have accounts here")
			break
		}
	}
}

// TestASuccessfulLoginGoesWhereTheUserWasHeading.
//
// Somebody who followed a link, was sent to sign in, and signed in should land
// on the page they wanted. The redirect target comes from the query string,
// which is why it is sanitised - see TestAPostLoginRedirectCannotLeaveTheSite.
func TestASuccessfulLoginGoesWhereTheUserWasHeading(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	cases := map[string]string{
		"":                      "/today",
		"/week?date=2026-03-16": "/week?date=2026-03-16",
		"https://evil.example":  "/today",
	}
	for next, want := range cases {
		t.Run("next="+next, func(t *testing.T) {
			form := url.Values{
				"email": {"admin@example.com"}, "password": {"a-long-enough-password"},
			}
			if next != "" {
				form.Set("next", next)
			}
			rec := post(t, srv, "/login", form)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("login = %d: %s", rec.Code, truncate(rec.Body.String()))
			}
			if got := rec.Header().Get("Location"); got != want {
				t.Errorf("landed on %q, want %q", got, want)
			}
		})
	}
}

// TestSigningOutRevokesTheSessionServerSide.
//
// Clearing the cookie is not enough: a cookie the browser has stopped sending
// is still a valid credential while it is in the sessions table, and somebody
// who copied it would still be signed in.
func TestSigningOutRevokesTheSessionServerSide(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	c := newClient(t, srv)

	if rec := c.get(t, "/today"); rec.Code != http.StatusOK {
		t.Fatalf("GET /today before signing out = %d", rec.Code)
	}

	rec := c.post(t, "/logout", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d: %s", rec.Code, truncate(rec.Body.String()))
	}
	if target := rec.Header().Get("Location"); target != "/login" {
		t.Errorf("signing out goes to %q, want /login", target)
	}

	// The same cookie, replayed, no longer works.
	if after := c.get(t, "/today"); after.Code == http.StatusOK {
		t.Error("the session still works after signing out, so the cookie was " +
			"only forgotten rather than revoked")
	}

	// Signing out twice, or with no session at all, is not an error: it is a
	// button in a tab somebody left open.
	if again := c.post(t, "/logout", url.Values{}); again.Code != http.StatusSeeOther {
		t.Errorf("signing out twice = %d, want a redirect", again.Code)
	}
	if none := post(t, srv, "/logout", url.Values{}); none.Code != http.StatusSeeOther {
		t.Errorf("signing out with no session = %d, want a redirect", none.Code)
	}

	// In local mode there is nothing to sign out of, and the route must not
	// pretend there is.
	local, _ := newTestServer(t)
	if rec := post(t, local, "/logout", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Errorf("signing out in local mode = %d, want a redirect to the app", rec.Code)
	}
}

// TestTheSessionCookieIsSecureExactlyWhenTheConnectionIs.
//
// Secure on a TLS connection, so the credential never travels in clear. *Not*
// secure on a plain-HTTP loopback run, because a Secure cookie would simply
// never be sent back and nothing would work at all - which is how a well-meant
// hardening change breaks local development.
//
// The proxy case is the one that needs care: the header is only believed from
// an address the configuration trusts, or anybody could claim TLS by sending a
// header.
func TestTheSessionCookieIsSecureExactlyWhenTheConnectionIs(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	login := func(t *testing.T, prepare func(*http.Request)) *http.Cookie {
		t.Helper()
		form := url.Values{
			"email": {"admin@example.com"}, "password": {"a-long-enough-password"},
		}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		prepare(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("login = %d: %s", rec.Code, truncate(rec.Body.String()))
		}
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				return cookie
			}
		}
		t.Fatal("login set no session cookie")
		return nil
	}

	plain := login(t, func(*http.Request) {})
	if plain.Secure {
		t.Error("the cookie is Secure on a plain-HTTP connection, so it would " +
			"never be sent back and nothing would work")
	}
	// Whatever the connection, the rest of the flags hold: no script may read
	// it, and it is not sent on a cross-site form post.
	if !plain.HttpOnly || plain.SameSite != http.SameSiteLaxMode {
		t.Errorf("the cookie's flags are HttpOnly=%v SameSite=%v, want true and Lax",
			plain.HttpOnly, plain.SameSite)
	}
	if plain.Domain != "" {
		t.Errorf("the cookie carries Domain=%q, so it is sent to sibling "+
			"subdomains somebody else may operate", plain.Domain)
	}

	overTLS := login(t, func(r *http.Request) {
		r.TLS = &tlsState
	})
	if !overTLS.Secure {
		t.Error("the cookie is not Secure over TLS, so the credential can travel in clear")
	}

	// An untrusted client claiming TLS is not believed. The test fixture
	// trusts no proxies, so this is any client.
	claimed := login(t, func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	if claimed.Secure {
		t.Error("a header from an untrusted client was enough to make the cookie " +
			"Secure, which means anybody can set that flag")
	}
}

// TestEveryScreenRendersAsAFragmentToo.
//
// A scripted client swaps part of the page in; a plain navigation gets the
// whole thing. Both come from the same definitions, which is what keeps the
// application usable with JavaScript disabled - and what means a fragment
// cannot quietly drift from the page around it.
func TestEveryScreenRendersAsAFragmentToo(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// The inline editors are fragment-only: the admin screen swaps a form in
	// place of a row.
	for _, path := range []string{
		"/customers/1/edit", "/projects/1/edit", "/assignments/1/edit",
	} {
		t.Run(path, func(t *testing.T) {
			rec := getHTMX(srv, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s as a fragment = %d: %s", path, rec.Code, truncate(rec.Body.String()))
			}
			body := rec.Body.String()
			if !strings.Contains(body, "<form") {
				t.Errorf("the fragment for %s is not a form:\n%s", path, truncate(body))
			}
			// A fragment, not a page: swapping a whole document into a table
			// row produces nested <html>, which browsers recover from in
			// surprising ways.
			if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "<body") {
				t.Errorf("the fragment for %s is a whole page:\n%s", path, truncate(body))
			}
		})
	}

	// Help is the other way round: both forms exist, and the panel is the
	// fragment.
	for _, screen := range []string{"today", "week", "month", "entries", "expenses"} {
		t.Run("help/"+screen, func(t *testing.T) {
			page := get(t, srv, "/help/"+screen)
			if page.Code != http.StatusOK {
				t.Fatalf("GET /help/%s = %d", screen, page.Code)
			}
			if !strings.Contains(page.Body.String(), "<body") {
				t.Errorf("the help page for %s is not a page", screen)
			}

			panel := getHTMX(srv, "/help/"+screen)
			if panel.Code != http.StatusOK {
				t.Fatalf("GET /help/%s as a fragment = %d", screen, panel.Code)
			}
			if strings.Contains(panel.Body.String(), "<body") {
				t.Errorf("the help panel for %s is a whole page", screen)
			}
			if panel.Body.Len() == 0 {
				t.Errorf("the help panel for %s is empty", screen)
			}
		})
	}
}

// getHTMX issues a GET announcing itself as a scripted client.
func getHTMX(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
