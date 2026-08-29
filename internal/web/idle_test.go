package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Idle observation over HTTP.
//
// The tests are about the boundary rather than the arithmetic, which is proved
// in the domain: what a browser may claim, what a person sees, and what a button
// actually does when pressed.

// seedIdleEntry records a stopped entry and returns the server and its id.
//
// Yesterday, deliberately. The test server runs on the real clock, so an entry
// placed on today would be partly in the future whenever the suite runs before
// three in the afternoon - and an observation is clamped to the present, so the
// test would pass in the evening and fail in the morning.
func seedIdleEntry(t *testing.T, start, end string) (*Server, string) {
	t.Helper()
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})

	rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {seededDay(srv)},
		"start": {start}, "end": {end}, "note": {"migration work"},
	})
	if rec.Code >= 400 {
		t.Fatalf("record entry = %d", rec.Code)
	}
	return srv, "1"
}

// seededDay is the day the fixture's entry lives on: yesterday, in the user's
// zone, which the fixture sets to UTC.
func seededDay(srv *Server) string {
	return srv.svc.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
}

// dayScreen is the day view of the seeded day, since the totals a test asserts
// on belong to that day rather than to whichever day the suite runs.
func dayScreen(srv *Server) string { return "/today?date=" + seededDay(srv) }

// observe posts what a page claims to have seen.
func observe(t *testing.T, srv *Server, entryID string, from, to time.Time, source string) int {
	t.Helper()
	return post(t, srv, "/idle", url.Values{
		"entry_id": {entryID},
		"from":     {from.Format(time.RFC3339)},
		"to":       {to.Format(time.RFC3339)},
		"source":   {source},
	}).Code
}

// atSeeded builds an instant on the seeded day, in UTC as the store keeps it.
func atSeeded(srv *Server, hour, minute int) time.Time {
	day := srv.svc.Now().UTC().AddDate(0, 0, -1)
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, time.UTC)
}

// TestIdleObservationBecomesAReviewOnTheDayScreen.
//
// The loop as a user meets it: the page reports a stretch, the screen asks about
// it with the numbers on the buttons, and pressing one changes the timesheet.
func TestIdleObservationBecomesAReviewOnTheDayScreen(t *testing.T) {
	srv, entry := seedIdleEntry(t, "09:00", "15:00")

	if code := observe(t, srv, entry,
		atSeeded(srv, 12, 0), atSeeded(srv, 13, 0), "asleep"); code != http.StatusNoContent {
		t.Fatalf("POST /idle = %d, want 204", code)
	}

	body := get(t, srv, dayScreen(srv)).Body.String()
	if !strings.Contains(body, "idle-review") {
		t.Fatal("the day screen does not show the observation")
	}
	// The sentence has to say what was seen, not that somebody was away.
	if !strings.Contains(body, "the page was not running") {
		t.Error("the observation should describe what the page saw, not conclude an absence")
	}
	// Both answers are offered, and each carries what it would leave behind.
	for _, want := range []string{"resolution\" value=\"keep\"",
		"resolution\" value=\"split\"", "resolution\" value=\"discard\""} {
		if !strings.Contains(body, want) {
			t.Errorf("the review is missing %q", want)
		}
	}

	rec := post(t, srv, "/idle/1/resolve", url.Values{"resolution": {"split"}})
	if rec.Code >= 400 {
		t.Fatalf("split = %d: %s", rec.Code, rec.Body.String())
	}

	after := get(t, srv, dayScreen(srv)).Body.String()
	if strings.Contains(after, "idle-review") {
		t.Error("the question is still being asked after it was answered")
	}
	// Six hours minus the observed one, across two entries.
	if !strings.Contains(after, "5:00") {
		t.Error("the day should total five hours after splitting an hour out of six")
	}
}

// TestIdleReviewIsAbsentUntilSomethingIsObserved.
//
// The panel is a prompt, and a prompt that appears when there is nothing to
// prompt about is noise. This is the case that would break silently if a
// template condition were dropped.
func TestIdleReviewIsAbsentUntilSomethingIsObserved(t *testing.T) {
	srv, _ := seedIdleEntry(t, "09:00", "15:00")

	if body := get(t, srv, dayScreen(srv)).Body.String(); strings.Contains(body, "idle-review") {
		t.Error("the review panel is rendered with nothing to review")
	}
}

// TestIdleReportsAreValidatedBeforeTheyAreBelieved.
//
// The times come from a browser, so the route has to refuse what it cannot make
// sense of - and must not answer 204 for a request it did not understand, which
// would leave a page reporting into a void.
func TestIdleReportsAreValidatedBeforeTheyAreBelieved(t *testing.T) {
	srv, entry := seedIdleEntry(t, "09:00", "15:00")

	cases := []struct {
		name string
		form url.Values
	}{
		{"no entry", url.Values{"from": {"2026-05-12T12:00:00Z"},
			"to": {"2026-05-12T13:00:00Z"}, "source": {"asleep"}}},
		{"unparseable times", url.Values{"entry_id": {entry},
			"from": {"lunchtime"}, "to": {"later"}, "source": {"asleep"}}},
		{"unknown source", url.Values{"entry_id": {entry},
			"from": {"2026-05-12T12:00:00Z"}, "to": {"2026-05-12T13:00:00Z"},
			"source": {"psychic"}}},
	}
	for _, c := range cases {
		if code := post(t, srv, "/idle", c.form).Code; code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", c.name, code)
		}
	}

	// A well-formed report the server declines on its merits - five minutes,
	// under the fifteen-minute threshold - is not the page's error.
	if code := observe(t, srv, entry,
		atSeeded(srv, 12, 0), atSeeded(srv, 12, 5), "asleep"); code != http.StatusNoContent {
		t.Errorf("a short stretch = %d, want 204 and no observation", code)
	}
	if body := get(t, srv, dayScreen(srv)).Body.String(); strings.Contains(body, "idle-review") {
		t.Error("a stretch under the threshold produced a question")
	}
}

// TestIdleResolutionRefusesAnUnknownDecision.
//
// The resolution arrives as a form field, and an unrecognised one must not be
// treated as any of the three - least of all as the one that removes time.
func TestIdleResolutionRefusesAnUnknownDecision(t *testing.T) {
	srv, entry := seedIdleEntry(t, "09:00", "15:00")
	observe(t, srv, entry, atSeeded(srv, 12, 0), atSeeded(srv, 13, 0), "asleep")

	if code := post(t, srv, "/idle/1/resolve",
		url.Values{"resolution": {"delete-everything"}}).Code; code != http.StatusBadRequest {
		t.Errorf("an unknown decision = %d, want 400", code)
	}
	if body := get(t, srv, dayScreen(srv)).Body.String(); !strings.Contains(body, "idle-review") {
		t.Error("the question should survive a decision the server did not understand")
	}
}

// TestIdleWatchIsWiredIntoThePage.
//
// The watcher needs two things rendered for it, and neither is visible, so
// nothing but a test notices when one goes missing: the threshold on the body,
// and the entry id on each running timer. Without the first the script does not
// watch at all; without the second it has nothing to report against.
func TestIdleWatchIsWiredIntoThePage(t *testing.T) {
	srv, _ := seedIdleEntry(t, "09:00", "15:00")
	post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}})

	body := get(t, srv, dayScreen(srv)).Body.String()
	if !regexp.MustCompile(`<body[^>]*data-idle-seconds="900"`).MatchString(body) {
		t.Error("the idle threshold is not rendered onto the body")
	}
	if !regexp.MustCompile(`class="running-item"[^>]*data-entry-id="\d+"`).MatchString(body) {
		t.Error("a running timer does not carry the entry id the watcher reports against")
	}

	// Switching the feature off removes the attribute, which is how the script
	// knows not to watch - so the switch has to reach the page, not only the
	// database.
	if rec := post(t, srv, "/settings", url.Values{
		"default_currency": {"SEK"}, "default_rounding": {"none"}, "default_rate": {"0"},
		"week_start": {"1"}, "max_timer_hours": {"12"},
	}); rec.Code >= 400 {
		t.Fatalf("save settings = %d", rec.Code)
	}
	if body := get(t, srv, dayScreen(srv)).Body.String(); strings.Contains(body, "data-idle-seconds") {
		t.Error("the threshold is still on the page after idle watching was switched off")
	}
}
