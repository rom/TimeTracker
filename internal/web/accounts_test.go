package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/service"
)

// Server-mode account administration and the proxy inbox.
//
// These only exist with an account service behind them, so they run on the
// server-mode fixture with real sessions and CSRF. Every one of them changes
// who can do what, or accepts somebody else's claim about your time, which is
// why they are worth asserting at the level a request reaches rather than only
// in the service.

// newClient is an authenticated administrator: a real session and the CSRF
// token the server rendered into its forms. The client type itself lives with
// the submit-and-approve tests, which needed it first.
func newClient(t *testing.T, srv *Server) *client {
	t.Helper()
	cookie := signIn(t, srv)
	return &client{srv: srv, cookie: cookie, token: csrfTokenFor(t, srv, cookie)}
}

// newClientWith signs in as the administrator with a given password, for the
// case where the password has just been changed.
func newClientWith(t *testing.T, srv *Server, password string) *client {
	t.Helper()
	rec := post(t, srv, "/login", url.Values{
		"email": {"admin@example.com"}, "password": {password},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login with %q = %d: %s", password, rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			return &client{srv: srv, cookie: cookie, token: csrfTokenFor(t, srv, cookie)}
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

// signInAs is a client for somebody other than the bootstrapped administrator.
//
// It goes through the account service rather than the login form because the
// form is the administrator's; the result is the same session cookie a browser
// would hold.
func signInAs(t *testing.T, srv *Server, accounts *service.Accounts, email, password string) *client {
	t.Helper()
	login, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: email, Password: password, IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("sign in as %s: %v", email, err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: login.CookieValue}
	return &client{srv: srv, cookie: cookie, token: csrfTokenFor(t, srv, cookie)}
}

// canSignIn reports whether an account's password still works, which is how the
// effect of deactivating it is observed.
func canSignIn(t *testing.T, accounts *service.Accounts, email, password string) bool {
	t.Helper()
	_, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: email, Password: password, IP: "203.0.113.9",
	})
	return err == nil
}

// TestTheLoginPageSendsAnAlreadySignedInBrowserOn.
//
// Landing on /login with a valid session is what happens when somebody
// bookmarks it or presses back after signing in. Showing the form again invites
// them to sign in a second time, which at best wastes a round trip and at worst
// reads as the first attempt having failed.
func TestTheLoginPageSendsAnAlreadySignedInBrowserOn(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	// Without a session the form is served, and carries no data.
	anonymous := get(t, srv, "/login")
	if anonymous.Code != http.StatusOK {
		t.Fatalf("GET /login unauthenticated = %d, want 200", anonymous.Code)
	}
	if !strings.Contains(anonymous.Body.String(), `name="password"`) {
		t.Error("the login page has no password field")
	}
	if strings.Contains(anonymous.Body.String(), "admin@example.com") {
		t.Error("the login page names an existing account")
	}

	c := newClient(t, srv)
	signedIn := c.get(t, "/login")
	if signedIn.Code != http.StatusSeeOther {
		t.Errorf("GET /login with a session = %d, want a redirect away from it", signedIn.Code)
	}

	// In local mode there are no accounts at all, so there is nothing to sign
	// in to and the page must not pretend otherwise.
	local, _ := newTestServer(t)
	if rec := get(t, local, "/login"); rec.Code != http.StatusSeeOther {
		t.Errorf("GET /login in local mode = %d, want a redirect - a local instance "+
			"has no accounts to sign in to", rec.Code)
	}
}

// TestOIDCRoutesAreAbsentWithoutAProvider.
//
// Both routes are registered in every mode, because the route table is one
// function. With no provider configured they must be absent rather than
// half-working: a login route that errors instead of 404ing tells somebody
// probing the instance that single sign-on is there but broken.
func TestOIDCRoutesAreAbsentWithoutAProvider(t *testing.T) {
	for name, build := range map[string]func(*testing.T) *Server{
		"local mode": func(t *testing.T) *Server {
			srv, _ := newTestServer(t)
			return srv
		},
		"server mode with password login": func(t *testing.T) *Server {
			srv, _ := newServerModeTestServer(t)
			return srv
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := build(t)
			for _, path := range []string{"/auth/oidc/start", "/auth/oidc/callback"} {
				rec := get(t, srv, path)
				if rec.Code != http.StatusNotFound {
					t.Errorf("GET %s with no provider = %d, want 404\n%s",
						path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// TestAccountAdministrationRoutesAreAbsentInLocalMode.
//
// Same registration, same reasoning, and here it matters more: these routes
// change roles and passwords. A local instance has a single user and no account
// service, and every one of them must be a flat 404.
func TestAccountAdministrationRoutesAreAbsentInLocalMode(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, route := range []struct {
		path string
		form url.Values
	}{
		{"/users/1", url.Values{"role": {"admin"}}},
		{"/users/1/password", url.Values{"new_password": {"a-long-enough-password"}}},
		{"/members", url.Values{"project_id": {"1"}, "user_id": {"1"}}},
		{"/members/remove", url.Values{"project_id": {"1"}, "user_id": {"1"}}},
	} {
		t.Run(route.path, func(t *testing.T) {
			rec := post(t, srv, route.path, route.form)
			if rec.Code != http.StatusNotFound {
				t.Errorf("POST %s in local mode = %d, want 404\n%s",
					route.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestChangingSomebodysRoleTakesEffect.
//
// A role is the whole permission model. The assertion that matters is the one
// after it: deactivating an account must stop it being usable, not merely take
// it off a list.
func TestChangingSomebodysRoleTakesEffect(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	c := newClient(t, srv)

	if rec := c.post(t, "/users", url.Values{
		"display_name": {"Member"}, "email": {"member@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create user = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := c.post(t, "/users/2", url.Values{
		"display_name": {"Renamed Member"}, "email": {"member@example.com"},
		"role": {"manager"}, "active": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("update user = %d: %s", rec.Code, rec.Body.String())
	}

	screen := c.get(t, "/users").Body.String()
	if !strings.Contains(screen, "Renamed Member") {
		t.Error("the rename is not on the users screen")
	}

	// Still able to sign in while active.
	if !canSignIn(t, accounts, "member@example.com", "a-long-enough-password") {
		t.Fatal("an active member cannot sign in, so the check below proves nothing")
	}

	// Deactivated - the checkbox simply absent, which is how an unchecked box
	// arrives - and the account stops working.
	if rec := c.post(t, "/users/2", url.Values{
		"display_name": {"Renamed Member"}, "email": {"member@example.com"},
		"role": {"manager"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate = %d: %s", rec.Code, rec.Body.String())
	}
	if canSignIn(t, accounts, "member@example.com", "a-long-enough-password") {
		t.Error("a deactivated account can still sign in")
	}

	// An unknown role is refused rather than stored: a role nothing recognises
	// would be a user the authoriser cannot reason about.
	if rec := c.post(t, "/users/2", url.Values{
		"display_name": {"Renamed Member"}, "email": {"member@example.com"},
		"role": {"superuser"}, "active": {"on"},
	}); rec.Code == http.StatusSeeOther {
		t.Error("a user was given a role that does not exist")
	}
}

// TestChangingAPasswordEndsEverySession.
//
// A password change is what somebody does when they think their password is
// known to someone else. If the old sessions kept working, it would achieve
// nothing - so the handler revokes them and sends the browser to the login
// page, which is also the honest thing to show.
func TestChangingAPasswordEndsEverySession(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	c := newClient(t, srv)

	// The session works before the change.
	if rec := c.get(t, "/today"); rec.Code != http.StatusOK {
		t.Fatalf("GET /today before the change = %d", rec.Code)
	}

	rec := c.post(t, "/users/1/password", url.Values{
		"current_password": {"a-long-enough-password"},
		"new_password":     {"an-even-longer-password"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("set password = %d: %s", rec.Code, rec.Body.String())
	}
	if target := rec.Header().Get("Location"); target != "/login" {
		t.Errorf("after changing a password the browser goes to %q, want /login", target)
	}
	// The response clears the cookie, so the browser is not left holding a
	// credential that no longer works.
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the response did not clear the session cookie")
	}

	// And the old session is dead on the server, which is the part a cleared
	// cookie alone would not give.
	if after := c.get(t, "/today"); after.Code == http.StatusOK {
		t.Error("the session from before the password change still works")
	}

	// The wrong current password changes nothing.
	fresh := newClientWith(t, srv, "an-even-longer-password")
	if rec := fresh.post(t, "/users/1/password", url.Values{
		"current_password": {"not-the-password"},
		"new_password":     {"another-long-password"},
	}); rec.Code == http.StatusSeeOther {
		t.Error("a password was changed without the current one")
	}
}

// TestProjectMembershipIsAddedAndRemoved.
//
// Membership is the scope every listing is filtered by, so removing somebody
// has to take their access with it rather than only tidy a screen.
func TestProjectMembershipIsAddedAndRemoved(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	c := newClient(t, srv)

	c.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}})
	c.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	c.post(t, "/users", url.Values{
		"display_name": {"Member"}, "email": {"member@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	})

	if rec := c.post(t, "/members", url.Values{
		"project_id": {"1"}, "user_id": {"2"}, "rate": {"95"}, "currency": {"EUR"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("add member = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(c.get(t, "/users").Body.String(), "Member") {
		t.Error("the added member is not on the screen")
	}

	if rec := c.post(t, "/members/remove", url.Values{
		"project_id": {"1"}, "user_id": {"2"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("remove member = %d: %s", rec.Code, rec.Body.String())
	}

	// Removing somebody who is not a member is not an error: it is a stale
	// button in a tab somebody left open.
	if rec := c.post(t, "/members/remove", url.Values{
		"project_id": {"1"}, "user_id": {"2"},
	}); rec.Code == http.StatusInternalServerError {
		t.Error("removing a non-member = 500, want a no-op or a refusal")
	}

	// An unreadable rate is the person's typo, not a membership at rate zero.
	if rec := c.post(t, "/members", url.Values{
		"project_id": {"1"}, "user_id": {"2"}, "rate": {"ninety-five"}, "currency": {"EUR"},
	}); rec.Code == http.StatusSeeOther {
		t.Error("a membership was created with an unreadable rate")
	}
}

// TestTheInboxAcceptsAndDeclinesWhatSomebodyElseClaimed.
//
// A proposal is somebody else's claim about your time. Accepting makes it
// count; declining keeps the record of what was claimed, because a proposal
// that vanished silently would leave its author wondering. Neither may be
// something a third party can do, which is what the scope on these routes is
// for.
func TestTheInboxAcceptsAndDeclinesWhatSomebodyElseClaimed(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)

	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	admin.post(t, "/assignments", url.Values{"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})
	admin.post(t, "/users", url.Values{
		"display_name": {"Member"}, "email": {"member@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	})
	for _, userID := range []string{"1", "2"} {
		admin.post(t, "/members", url.Values{"project_id": {"1"}, "user_id": {userID}})
	}

	// Two proposals for the colleague: one to accept, one to decline.
	for _, start := range []string{"09:00", "13:00"} {
		if rec := admin.post(t, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {start},
			"duration": {"2h"}, "on_behalf_of": {"2"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("propose at %s = %d: %s", start, rec.Code, rec.Body.String())
		}
	}

	member := signInAs(t, srv, accounts, "member@example.com", "a-long-enough-password")

	inbox := member.get(t, "/inbox")
	if inbox.Code != http.StatusOK {
		t.Fatalf("GET /inbox = %d: %s", inbox.Code, inbox.Body.String())
	}
	if !strings.Contains(inbox.Body.String(), "Acme") {
		t.Fatal("the inbox shows no proposal, so this proves nothing")
	}

	if rec := member.post(t, "/inbox/entries/1/accept", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := member.post(t, "/inbox/entries/2/reject", url.Values{
		"reason": {"I was not there"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("reject = %d: %s", rec.Code, rec.Body.String())
	}

	// The inbox is empty: both have been decided.
	if body := member.get(t, "/inbox").Body.String(); strings.Contains(body, "/accept") {
		t.Error("a decided proposal is still waiting in the inbox")
	}

	// The accepted one counts towards the member's day and the declined one
	// does not - four hours were proposed, two accepted.
	day := member.get(t, "/today?date=2026-03-16").Body.String()
	if !strings.Contains(day, "2h 00m") {
		t.Errorf("the accepted proposal does not count towards the day:\n%s", day)
	}

	// Deciding something that is not yours is refused. The administrator who
	// wrote the proposals is not the person whose time they are about.
	for _, path := range []string{
		"/inbox/entries/1/accept", "/inbox/entries/2/reject",
	} {
		rec := admin.post(t, path, url.Values{})
		if rec.Code == http.StatusSeeOther {
			t.Errorf("POST %s by the proposal's author was accepted - only the "+
				"person whose time it is may decide", path)
		}
	}
}

// TestDecidingAProposalThatIsNotThereIsRefused covers the stale-tab case on all
// four inbox routes at once: the row was already decided, or never existed.
func TestDecidingAProposalThatIsNotThereIsRefused(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	c := newClient(t, srv)

	for _, path := range []string{
		"/inbox/entries/999/accept", "/inbox/entries/999/reject",
		"/inbox/expenses/999/accept", "/inbox/expenses/999/reject",
	} {
		t.Run(path, func(t *testing.T) {
			rec := c.post(t, path, url.Values{})
			if rec.Code == http.StatusSeeOther {
				t.Errorf("POST %s reported success for something that does not exist", path)
			}
			if rec.Code == http.StatusInternalServerError {
				t.Errorf("POST %s = 500, want a refusal", path)
			}
		})
	}
}
