package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/service"
)

// Regression tests.
//
// Every test here corresponds to a defect that actually reached a running
// build. They are gathered in one file, each named for its symptom rather than
// for the code it touches, because the point of a regression test is to fail if
// the same mistake is made again - not to document a module.
//
// A bug without a test here is a bug that will come back.

// A completed customer form was rejected with "customer name is required".
//
// fetch(FormData) sends multipart/form-data. r.ParseForm does not parse a
// multipart body, but it *does* set r.Form - so the later r.FormValue never
// falls back to the multipart parser and every field arrived empty. The handler
// then reported the form as blank when the user had filled it in completely.
func TestRegressionMultipartFormFieldsAreNotLost(t *testing.T) {
	srv, _ := newTestServer(t)

	var body strings.Builder
	const boundary = "----regression"
	for _, field := range []struct{ name, value string }{
		{"name", "MCF"},
		{"currency", "SEK"},
		{"colour_key", "blue"},
	} {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(`Content-Disposition: form-data; name="` + field.name + `"` + "\r\n\r\n")
		body.WriteString(field.value + "\r\n")
	}
	body.WriteString("--" + boundary + "--\r\n")

	req := httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a completed multipart form was rejected: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(get(t, srv, "/admin").Body.String(), "MCF") {
		t.Error("the customer was not created")
	}
}

// Every screen with entries returned 500 after the templates were translated.
//
// entry-row is rendered inside a range and receives a dict, so $ no longer
// resolved to the page data and {{$.T ...}} failed at execution time. The
// failure was invisible in tests because the test logger discards output, and
// the only symptom was a 500 on any page that had at least one entry.
func TestRegressionPagesWithEntriesRender(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}})

	// The bug only appeared once there was something to render in the row
	// fragment, so an empty-page smoke test would not have caught it.
	for _, path := range []string{"/today", "/entries", "/week"} {
		rec := get(t, srv, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s with entries = %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "<table") &&
			!strings.Contains(rec.Body.String(), "week-grid") {
			t.Errorf("GET %s rendered no content", path)
		}
	}
}

// The day view went blank the moment scoped queries landed.
//
// Day and Week called the store directly with a zero Scope, which renders as
// "match nothing" - by design, so a forgotten scope fails safe. The lesson is
// the test, not the fix: any screen that lists records must be exercised with
// records present.
func TestRegressionScopedQueriesReturnTheUsersOwnData(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"Findable"}, "billable": {"on"}})
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "duration": {"1h"}, "billable": {"on"},
	})

	for _, path := range []string{"/today", "/week", "/entries"} {
		body := get(t, srv, path).Body.String()
		if !strings.Contains(body, "Findable") {
			t.Errorf("GET %s does not show the user's own entry", path)
		}
	}
}

// PDF and DOCX export returned 501 long after they were implemented, because
// the handler's switch still listed them as unimplemented.
func TestRegressionAllFourExportFormatsWork(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct{ format, prefix, contentType string }{
		{"pdf", "%PDF-", "application/pdf"},
		{"docx", "PK\x03\x04", "application/vnd.openxmlformats"},
		{"csv", "\xef\xbb\xbf", "text/csv"},
		{"json", "{", "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			rec := get(t, srv, "/export/"+tc.format)
			if rec.Code != http.StatusOK {
				t.Fatalf("export = %d", rec.Code)
			}
			if !strings.HasPrefix(rec.Body.String(), tc.prefix) {
				t.Errorf("the %s export does not begin as that format", tc.format)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), tc.contentType) {
				t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// SECURITY.md claimed the sniffed content type must agree with the file
// extension, and no such check existed: a shell script named .png was stored.
// The claim is now true, and this is what keeps it true.
func TestRegressionExtensionMustMatchContent(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/expenses", url.Values{
		"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"description": {"Taxi"}, "amount": {"100"}, "billable": {"on"},
	})

	upload := func(filename, content string) int {
		var body strings.Builder
		const boundary = "----upload"
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(`Content-Disposition: form-data; name="file"; filename="` +
			filename + `"` + "\r\n")
		body.WriteString("Content-Type: application/octet-stream\r\n\r\n")
		body.WriteString(content + "\r\n")
		body.WriteString("--" + boundary + "--\r\n")

		req := httptest.NewRequest(http.MethodPost, "/attachments/expense/1",
			strings.NewReader(body.String()))
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := upload("innocent.png", "#!/bin/sh\necho hello\n"); code == http.StatusSeeOther {
		t.Error("a shell script named .png was accepted")
	}
	if code := upload("notes.txt", "these are some notes"); code != http.StatusSeeOther {
		t.Errorf("an honest text file was refused: %d", code)
	}
}

// The macOS and Windows builds broke because a symbol used by shared code was
// defined only in a _linux.go file. No test can catch that - only a
// cross-compile can - so `make build-check` exists and runs in `make check`.
// This test asserts the guard is still wired in, since a Makefile edit could
// silently remove it.
func TestRegressionCrossCompileCheckIsWiredIn(t *testing.T) {
	makefile, err := readRepoFile("Makefile")
	if err != nil {
		t.Skipf("cannot read the Makefile: %v", err)
	}
	if !strings.Contains(makefile, "build-check:") {
		t.Error("the cross-compile target is gone")
	}
	// It has to be part of `check`, not merely present.
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, "check:") {
			if !strings.Contains(line, "build-check") {
				t.Errorf("`make check` no longer cross-compiles: %s", line)
			}
			return
		}
	}
	t.Error("no check target found")
}

// readRepoFile reads a file from the repository root, which is two levels up
// from this package.
func readRepoFile(name string) (string, error) {
	content, err := os.ReadFile("../../" + name)
	return string(content), err
}

// Editing an entry existed in the service and had routes, but no screen ever
// rendered a link to it: the only controls on a row were stop and delete. The
// feature was reachable only by typing a URL, which is the same as absent.
//
// The test asserts the link, not the handler, because the handler was never the
// broken part.
func TestRegressionEntryRowsOfferAnEditControl(t *testing.T) {
	srv, _ := newTestServer(t)
	seedEntry(t, srv)

	for _, path := range []string{"/today", "/entries"} {
		body := get(t, srv, path).Body.String()
		if !strings.Contains(body, `href="/entries/1/edit"`) {
			t.Errorf("%s renders no way to correct an entry", path)
		}
	}
}

// The correction this feature exists for: eight minutes recorded where eight
// hours were meant.
//
// The edit form prefills the duration and deliberately leaves the end time
// empty. An end time takes precedence over a duration in the handler, so a
// prefilled one would silently discard the corrected duration - the user would
// save "8h", see "8m", and have no idea why.
func TestRegressionCorrectingADurationIsNotOverriddenByAnEndTime(t *testing.T) {
	srv, _ := newTestServer(t)
	today := seedEntry(t, srv)

	// Eight minutes, entered by hand.
	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"8m"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create entry = %d: %s", rec.Code, rec.Body.String())
	}

	form := get(t, srv, "/entries/2/edit")
	if form.Code != http.StatusOK {
		t.Fatalf("GET the edit form = %d: %s", form.Code, form.Body.String())
	}
	body := form.Body.String()
	// The duration is shown in the notation it accepts, so what is on screen can
	// be typed back.
	if !strings.Contains(body, `name="duration" inputmode="text"`) || !strings.Contains(body, `value="8m"`) {
		t.Errorf("the edit form does not show the recorded duration in editable form:\n%s", body)
	}
	// The end time is present but empty, which is what stops it winning.
	if !strings.Contains(body, `id="edit-end" name="end" value=""`) {
		t.Error("the end time is prefilled; a corrected duration would be discarded")
	}

	if rec := post(t, srv, "/entries/2", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"8h"}, "end": {""}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save the correction = %d: %s", rec.Code, rec.Body.String())
	}

	day := get(t, srv, "/today?date="+today).Body.String()
	if strings.Contains(day, "0h 08m") || strings.Contains(day, "8m") {
		t.Errorf("the entry is still eight minutes after being corrected to eight hours:\n%s", day)
	}
	if !strings.Contains(day, "8h") {
		t.Errorf("the corrected duration is not shown:\n%s", day)
	}
}

// seedEntry creates the catalogue and one manual entry, and returns the date it
// used. The test server runs on the real clock, so the entry is placed today -
// a fixed date would fall outside the day view's default range and the screens
// would render empty for the wrong reason.
func seedEntry(t *testing.T, srv *Server) string {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}, "rate": {"1250"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"08:00"},
		"duration": {"1h"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("seed entry = %d: %s", rec.Code, rec.Body.String())
	}
	return today
}

// The header clock showed "--:--:--" and never updated.
//
// It was a placeholder waiting for JavaScript to replace it, which made three
// separate failures indistinguishable and all of them silent: the script not
// running, the script erroring before it got that far, and the clock itself
// being broken. It also read the *browser's* zone, so it could disagree with
// every other time on the page.
//
// The time is now rendered by the server, in the user's own zone. Script only
// keeps it moving, so the worst case is a clock that is stale rather than one
// that is visibly broken.
func TestRegressionHeaderClockIsServerRendered(t *testing.T) {
	srv, _ := newTestServer(t)

	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `id="header-clock"`) {
		t.Fatal("the header clock is not rendered at all")
	}
	if strings.Contains(body, "--:--:--") {
		t.Error("the header clock is still a placeholder for a script to fill in")
	}

	// The rendered value must be a real time, and it must be this instant's -
	// a hard-coded string would satisfy the check above.
	rendered := between(t, body, `id="header-clock"`, ">", "<")
	parsed, err := time.Parse("15:04:05", rendered)
	if err != nil {
		t.Fatalf("the header clock rendered %q, which is not a time: %v", rendered, err)
	}
	now := time.Now().UTC()
	wanted := time.Date(0, 1, 1, now.Hour(), now.Minute(), now.Second(), 0, time.UTC)
	if diff := parsed.Sub(wanted); diff > time.Minute || diff < -time.Minute {
		t.Errorf("the header clock rendered %q, which is not the current time (%s)",
			rendered, wanted.Format("15:04:05"))
	}

	// The zone travels with it, so the script ticks in the user's zone rather
	// than the browser's.
	if !strings.Contains(body, `data-zone=`) {
		t.Error("the header clock carries no zone; a script would tick it in the browser's")
	}
}

// between returns the text between open and close, after the anchor.
func between(t *testing.T, body, anchor, open, close string) string {
	t.Helper()
	at := strings.Index(body, anchor)
	if at < 0 {
		t.Fatalf("anchor %q not found", anchor)
	}
	rest := body[at:]
	start := strings.Index(rest, open)
	if start < 0 {
		t.Fatalf("no %q after %q", open, anchor)
	}
	rest = rest[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		t.Fatalf("no %q after %q", close, anchor)
	}
	return strings.TrimSpace(rest[:end])
}

// Every screen with a proposal in the inbox returned 500.
//
// A custom template function named "index" shadowed the Go template builtin of
// the same name. The builtin does a map or slice lookup; the custom one took a
// []int64. So `index $page.Inbox.Overlapping .ID` - a perfectly ordinary map
// lookup - failed at render time with "wrong type for value; expected []int64",
// an error that says nothing about the real cause.
//
// It only appeared when the inbox had something in it, which is why it survived
// a smoke test: the page renders fine while it is empty.
//
// The function is now called "nth", and this test keeps a proposal in the inbox
// so the line is actually evaluated.
func TestRegressionInboxRendersWithProposals(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	cookie := signIn(t, srv)
	token := csrfTokenFor(t, srv, cookie)

	postAs := func(path string, form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf_token", token)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	postAs("/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	postAs("/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	postAs("/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	postAs("/users", url.Values{
		"display_name": {"Member"}, "email": {"member@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	})
	for _, userID := range []string{"1", "2"} {
		postAs("/members", url.Values{"project_id": {"1"}, "user_id": {userID}})
	}

	// Two proposals for the colleague, overlapping - which is what puts an
	// entry in the map the template looks up.
	today := time.Now().UTC().Format("2006-01-02")
	for _, start := range []string{"09:00", "09:30"} {
		if rec := postAs("/entries", url.Values{
			"assignment_id": {"1"}, "date": {today}, "start": {start},
			"duration": {"2h"}, "on_behalf_of": {"2"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("propose at %s = %d: %s", start, rec.Code, rec.Body.String())
		}
	}

	login, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: "member@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("member login: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: login.CookieValue})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inbox with proposals = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Acme") {
		t.Error("the inbox rendered but shows no proposal")
	}
}

// The expenses screen returned 500 as soon as it had an expense on it, for the
// same shadowed-builtin reason. Both pages are covered because both look a row
// up in a map, and a fix to one would not have fixed the other.
func TestRegressionExpensesScreenRendersWithRows(t *testing.T) {
	srv, _ := newTestServer(t)
	today := seedEntry(t, srv)

	if rec := post(t, srv, "/expenses", url.Values{
		"project_id": {"1"}, "spent_on": {today}, "category": {"Travel"},
		"description": {"Taxi"}, "amount": {"250"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create expense = %d: %s", rec.Code, rec.Body.String())
	}

	rec := get(t, srv, "/expenses")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /expenses with rows = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Taxi") {
		t.Error("the expenses screen rendered but shows no expense")
	}
}
