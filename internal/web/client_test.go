package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/service"
)

// A client, over HTTP, in server mode.
//
// The service tests prove the value handed out is narrowed. These ask the
// question from outside: what does somebody with a client login actually get
// back from every screen and every download - which is where a route that
// forgot to authorise, or a screen that renders something the service happened
// to include, would show up.

// clientSession signs in a client user and returns a driver for it, along with
// an admin driver and the internal note seeded into the data.
func clientSession(t *testing.T) (adminClient, clientClient *client, note string) {
	admin, viewer, note := clientSessionRaw(t)
	viewer.token = anyTokenFor(t, viewer)
	return admin, viewer, note
}

// clientSessionRaw is clientSession without the CSRF lookup.
func clientSessionRaw(t *testing.T) (adminClient, clientClient *client, note string) {
	t.Helper()
	srv, accounts := newServerModeTestServer(t)

	adminCookie := signIn(t, srv)
	admin := &client{srv: srv, cookie: adminCookie, token: csrfTokenFor(t, srv, adminCookie)}

	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}, "rate": {"1250"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	admin.post(t, "/assignments", url.Values{"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})
	admin.post(t, "/members", url.Values{"project_id": {"1"}, "user_id": {"1"}})

	note = "INTERNAL-they-have-not-paid-for-March"
	today := time.Now().UTC().Format("2006-01-02")
	if rec := admin.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"2h"}, "billable": {"on"}, "note": {note},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record time = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := admin.post(t, "/users", url.Values{
		"display_name": {"The Client"}, "email": {"client@example.com"},
		"password": {"a-long-enough-password"}, "role": {"client"},
		"client_customer_id": {"1"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create the client = %d: %s", rec.Code, rec.Body.String())
	}

	login, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: "client@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("client login: %v", err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: login.CookieValue}
	viewer := &client{srv: srv, cookie: cookie}
	return admin, viewer, note
}

// TestClientSeesTheirWorkAndNotTheInternals.
//
// The whole point of the role, from the outside: the hours are there, and
// nothing that was written for colleagues is.
func TestClientSeesTheirWorkAndNotTheInternals(t *testing.T) {
	_, viewer, note := clientSession(t)

	body := viewer.get(t, "/entries").Body.String()
	if !strings.Contains(body, "Development") {
		t.Error("a client cannot see the work done for them")
	}
	if strings.Contains(body, note) {
		t.Error("the internal note is on the client's screen")
	}
	// The customer's negotiated rate, and the amount it produced.
	for _, money := range []string{"1250", "2500"} {
		if strings.Contains(body, money) {
			t.Errorf("a money figure (%s) reached the client's screen", money)
		}
	}
	// And no controls that would answer 403: a report full of buttons that
	// refuse reads as broken rather than as read-only.
	for _, control := range []string{"/edit", "/delete"} {
		if strings.Contains(body, control) {
			t.Errorf("a client's row offers %s", control)
		}
	}
}

// TestClientDownloadsAreNarrowedToo.
//
// Every format, because the export writers are handed the same values and a
// leak here travels: a file gets forwarded in a way a screen does not.
func TestClientDownloadsAreNarrowedToo(t *testing.T) {
	_, viewer, note := clientSession(t)

	from := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	to := time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02")

	for _, format := range []string{"csv", "json", "md"} {
		rec := viewer.get(t, "/export/"+format+"?from="+from+"&to="+to)
		if rec.Code != http.StatusOK {
			t.Errorf("/export/%s = %d for a client", format, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Development") {
			t.Errorf("/export/%s: the client's own work is missing", format)
		}
		if strings.Contains(body, note) {
			t.Errorf("/export/%s carries the internal note", format)
		}
		if strings.Contains(body, "1250") || strings.Contains(body, "2500") {
			t.Errorf("/export/%s carries money", format)
		}
	}
}

// TestClientCannotDownloadABackup.
//
// The route that was open. A backup carries the catalogue whole, including the
// customer's negotiated hourly rate, and the check on it asked whether the
// actor could view their customer's time - which a client can. It came back
// with the rate in it.
func TestClientCannotDownloadABackup(t *testing.T) {
	_, viewer, _ := clientSession(t)

	rec := viewer.get(t, "/backup/download")
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "1250") {
		t.Error("a client downloaded a backup containing the customer's rate")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("a client downloaded a %d-byte backup", rec.Body.Len())
	}
	// Refused before the response is committed to, so it is a refusal rather
	// than a 200 carrying an empty file.
	if rec.Code != http.StatusForbidden {
		t.Errorf("/backup/download = %d for a client, want 403", rec.Code)
	}
	// And the screen that lists the archives on disk.
	if rec := viewer.get(t, "/backup"); rec.Code != http.StatusForbidden {
		t.Errorf("/backup = %d for a client, want 403", rec.Code)
	}
}

// TestClientCannotChangeAnything.
//
// Read-only is the role's first rule, and it is worth asserting at the boundary
// rather than trusting that every handler asked: a refusal that happens in the
// service is only a refusal if the route reaches the service.
func TestClientCannotChangeAnything(t *testing.T) {
	_, viewer, _ := clientSession(t)

	today := time.Now().UTC().Format("2006-01-02")
	for _, attempt := range []struct {
		path string
		form url.Values
	}{
		{"/entries", url.Values{"assignment_id": {"1"}, "date": {today},
			"start": {"09:00"}, "duration": {"1h"}}},
		{"/timers/start", url.Values{"assignment_id": {"1"}}},
		{"/customers", url.Values{"name": {"Theirs"}, "currency": {"SEK"}}},
		{"/settings", url.Values{"default_currency": {"USD"}, "default_rounding": {"none"},
			"default_rate": {"0"}, "week_start": {"1"}}},
		{"/users", url.Values{"display_name": {"Me"}, "email": {"me@example.com"},
			"password": {"a-long-enough-password"}, "role": {"admin"}}},
	} {
		rec := viewer.post(t, attempt.path, attempt.form)
		if rec.Code < 400 {
			t.Errorf("POST %s = %d for a client; the role is read-only",
				attempt.path, rec.Code)
		}
	}
}

// anyTokenFor finds a CSRF token on any screen a client can reach.
//
// It has to look around, because a client's screens carry very few forms - there
// is almost nothing for them to submit, which is the role working. The token is
// wanted so the read-only test refuses for the right reason: a POST without one
// is rejected as a forged request, which would pass the test while proving
// nothing about authorisation.
func anyTokenFor(t *testing.T, viewer *client) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	for _, path := range []string{"/today", "/entries", "/week", "/help/today"} {
		body := viewer.get(t, path).Body.String()
		if start := strings.Index(body, marker); start >= 0 {
			rest := body[start+len(marker):]
			if end := strings.Index(rest, `"`); end > 0 {
				return rest[:end]
			}
		}
	}
	t.Fatal("no CSRF token on any screen a client can reach; the read-only test " +
		"would then be proving CSRF rather than authorisation")
	return ""
}

// TestClientIsOfferedOnlyWhatTheyHave.
//
// Hiding a link is presentation and never the enforcement - the screens refuse
// on their own, and the data behind them is narrowed either way. What this
// checks is that a client is not shown a menu of six links that all answer 403,
// and that their front door goes somewhere.
func TestClientIsOfferedOnlyWhatTheyHave(t *testing.T) {
	_, viewer, _ := clientSession(t)

	nav := viewer.get(t, "/entries").Body.String()
	for _, offered := range []string{`href="/entries"`, `href="/guide"`} {
		if !strings.Contains(nav, offered) {
			t.Errorf("a client is not offered %s", offered)
		}
	}
	for _, hidden := range []string{`href="/today"`, `href="/week"`, `href="/admin"`,
		`href="/users"`, `href="/approvals"`, `href="/inbox"`, `href="/expenses"`} {
		if strings.Contains(nav, hidden) {
			t.Errorf("a client is offered %s, which answers 403", hidden)
		}
	}

	// The front door. "/" is the day screen, which a client has no version of.
	rec := viewer.get(t, "/")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("GET / = %d for a client, want a redirect to their screen", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/entries" {
		t.Errorf("GET / sent a client to %q, want /entries", location)
	}
}

// TestClientAdministrativeScreensRefuse.
//
// Each of these either offers controls the role cannot use or reports on the
// instance rather than on their work. The catalogue is the one that mattered:
// before this it rendered for a client, with the customer's negotiated rate in
// it.
func TestClientAdministrativeScreensRefuse(t *testing.T) {
	_, viewer, _ := clientSession(t)

	for _, path := range []string{"/admin", "/approvals", "/approvals/report",
		"/reports/budgets", "/backup", "/users"} {
		if code := viewer.get(t, path).Code; code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("%s = %d for a client, want a refusal", path, code)
		}
	}
}
