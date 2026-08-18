package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/service"
)

// Weekly submit and approve, through HTTP.
//
// The service tests prove the rules; these prove they are reachable, and that
// the screen and the enforcement agree about what is offered. A control that
// appears but is refused, or a rule that holds but has no control, are both
// defects that only show at this layer.

// asUser issues a request carrying a session cookie and a valid CSRF token.
type client struct {
	srv    *Server
	cookie *http.Cookie
	token  string
}

func (c *client) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	c.srv.ServeHTTP(rec, req)
	return rec
}

func (c *client) post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set("csrf_token", c.token)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	c.srv.ServeHTTP(rec, req)
	return rec
}

// TestSubmitAndApproveThroughHTTP walks the loop two people actually walk:
// a member records a week and submits it, an administrator sees it waiting,
// sends it back, and finally approves the corrected version.
func TestSubmitAndApproveThroughHTTP(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)

	adminCookie := signIn(t, srv)
	admin := &client{srv: srv, cookie: adminCookie, token: csrfTokenFor(t, srv, adminCookie)}

	// The catalogue, and a member who is on the project.
	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}, "rate": {"1250"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	admin.post(t, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	if rec := admin.post(t, "/users", url.Values{
		"display_name": {"Member"}, "email": {"member@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create the member = %d: %s", rec.Code, rec.Body.String())
	}
	for _, userID := range []string{"1", "2"} {
		admin.post(t, "/members", url.Values{"project_id": {"1"}, "user_id": {userID}})
	}

	login, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: "member@example.com", Password: "a-long-enough-password", IP: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("member login: %v", err)
	}
	memberCookie := &http.Cookie{Name: auth.SessionCookieName, Value: login.CookieValue}
	member := &client{srv: srv, cookie: memberCookie}
	member.token = tokenFromPage(t, member.get(t, "/today").Body.String())

	today := time.Now().UTC().Format("2006-01-02")
	if rec := member.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"2h"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("member records time = %d: %s", rec.Code, rec.Body.String())
	}

	// The week view offers the submit control, and nothing else yet.
	week := member.get(t, "/week").Body.String()
	if !strings.Contains(week, `action="/week/submit"`) {
		t.Fatal("the week view offers no way to submit")
	}
	if strings.Contains(week, `action="/approvals/approve"`) {
		t.Error("the week view offers its owner an approve control")
	}

	if rec := member.post(t, "/week/submit", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("submit = %d: %s", rec.Code, rec.Body.String())
	}

	// Submitted means locked: the member's next entry in the same week is
	// refused, and the refusal says what to do about it.
	rec := member.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"14:00"}, "duration": {"1h"},
	})
	if rec.Code == http.StatusSeeOther {
		t.Fatal("time was added to a submitted week")
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "withdraw") {
		t.Errorf("the refusal does not say how to unlock the week: %s", rec.Body.String())
	}

	// The administrator sees it waiting; the member does not see their own.
	queue := admin.get(t, "/approvals")
	if queue.Code != http.StatusOK {
		t.Fatalf("GET /approvals = %d: %s", queue.Code, queue.Body.String())
	}
	if !strings.Contains(queue.Body.String(), "Member") {
		t.Error("the submitted week is not in the approval queue")
	}
	ownQueue := member.get(t, "/approvals").Body.String()
	if strings.Contains(ownQueue, `action="/approvals/approve"`) {
		t.Error("the submitter was offered a control to approve their own week")
	}

	weekStart := weekStartFromPage(t, queue.Body.String())

	// Rejected without a reason is refused; with one it goes back.
	if rec := admin.post(t, "/approvals/reject", url.Values{
		"user_id": {"2"}, "week_start": {weekStart},
	}); rec.Code == http.StatusSeeOther {
		t.Error("a week was sent back with no reason")
	}
	if rec := admin.post(t, "/approvals/reject", url.Values{
		"user_id": {"2"}, "week_start": {weekStart}, "reason": {"Thursday is on the wrong project"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("reject = %d: %s", rec.Code, rec.Body.String())
	}

	// The reason reaches the member on the screen where they will fix it.
	corrected := member.get(t, "/week").Body.String()
	if !strings.Contains(corrected, "Thursday is on the wrong project") {
		t.Error("the rejection reason is not shown to the person who has to act on it")
	}
	if rec := member.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"14:00"}, "duration": {"1h"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("a rejected week is still locked: %d: %s", rec.Code, rec.Body.String())
	}

	// Resubmitted and approved.
	if rec := member.post(t, "/week/submit", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("resubmit = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := admin.post(t, "/approvals/approve", url.Values{
		"user_id": {"2"}, "week_start": {weekStart},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("approve = %d: %s", rec.Code, rec.Body.String())
	}

	// An approved week is beyond the owner's reach, including withdrawal.
	if rec := member.post(t, "/week/withdraw", url.Values{}); rec.Code == http.StatusSeeOther {
		t.Error("an approved week was withdrawn by its owner")
	}

	// And the way back exists on the approvals screen, not only in the service.
	decided := admin.get(t, "/approvals").Body.String()
	if !strings.Contains(decided, `action="/approvals/reopen"`) {
		t.Error("an approved week offers no way to reopen it")
	}
	if rec := admin.post(t, "/approvals/reopen", url.Values{
		"user_id": {"2"}, "week_start": {weekStart}, "reason": {"an hour was on the wrong project"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("reopen = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := member.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"16:00"}, "duration": {"30m"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("the reopened week still refuses changes: %d: %s", rec.Code, rec.Body.String())
	}
}

// TestLocalModeOffersNoApproval: with a single account there is nobody else to
// approve, so the navigation does not pretend otherwise - and the screen behind
// it still answers honestly rather than erroring.
func TestLocalModeOffersNoApproval(t *testing.T) {
	srv, _ := newTestServer(t)
	seedEntry(t, srv)

	if body := get(t, srv, "/today").Body.String(); strings.Contains(body, `href="/approvals"`) {
		t.Error("the single-user build advertises an approvals screen")
	}
	if body := get(t, srv, "/week").Body.String(); strings.Contains(body, `action="/approvals/approve"`) {
		t.Error("the single-user build offers an approve control")
	}
}

// tokenFromPage extracts a CSRF token from any rendered page.
func tokenFromPage(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("no CSRF token in the rendered page")
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("the CSRF token is not terminated")
	}
	return rest[:end]
}

// weekStartFromPage reads the week the approval queue is offering a decision on,
// rather than recomputing it - which would only test that two copies of the same
// arithmetic agree.
func weekStartFromPage(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="week_start" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("the approval queue names no week: %s", body)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("the week start is not terminated")
	}
	return rest[:end]
}
