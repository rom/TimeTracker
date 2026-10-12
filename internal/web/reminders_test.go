package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// End-of-day and end-of-week nudges over HTTP.
//
// The service tests cover when a nudge is true. These cover the two things only
// the HTTP layer can get wrong: which screens show the panel, and whether the
// dismiss control reaches the right row.

// eveningServer builds a server whose clock reads a fixed evening, so the
// end-of-day window is open regardless of when the suite runs.
func eveningServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := newTestServerAt(t, time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC))

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})
	return srv
}

// TestRemindersAppearOnTheDayAndWeekScreens.
//
// And nowhere else. A nudge belongs where somebody can act on it; on the
// catalogue screen it is furniture.
func TestRemindersAppearOnTheDayAndWeekScreens(t *testing.T) {
	srv := eveningServer(t)

	for _, path := range []string{"/today", "/week"} {
		if body := get(t, srv, path).Body.String(); !strings.Contains(body, "reminder-list") {
			t.Errorf("%s does not show the reminder panel", path)
		}
	}
	if body := get(t, srv, "/admin").Body.String(); strings.Contains(body, "reminder-list") {
		t.Error("the catalogue screen shows reminders; they belong where they can be acted on")
	}
}

// TestRemindersAreAboutNowNotAboutTheDayBeingViewed.
//
// The day screen browses history. "Nothing recorded today yet", rendered under
// a day three weeks ago, is a puzzle rather than a prompt.
func TestRemindersAreAboutNowNotAboutTheDayBeingViewed(t *testing.T) {
	srv := eveningServer(t)

	if body := get(t, srv, "/today").Body.String(); !strings.Contains(body, "reminder-list") {
		t.Fatal("expected the panel on today")
	}
	if body := get(t, srv, "/today?date=2026-04-20").Body.String(); strings.Contains(body, "reminder-list") {
		t.Error("a past day shows nudges about today")
	}
	if body := get(t, srv, "/week?date=2026-04-20").Body.String(); strings.Contains(body, "reminder-list") {
		t.Error("a past week shows nudges about this one")
	}
}

// TestDismissingAReminderHidesIt.
//
// The one write the feature has, and the one that would silently do nothing if
// the scope went missing from the form.
func TestDismissingAReminderHidesIt(t *testing.T) {
	srv := eveningServer(t)

	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, "reminder.emptyday") && !strings.Contains(body, "Nothing recorded today") {
		t.Fatalf("expected an empty-day nudge; got none")
	}

	rec := post(t, srv, "/reminders/empty_day/dismiss", url.Values{"scope": {"2026-05-13"}})
	if rec.Code >= 400 {
		t.Fatalf("dismiss = %d: %s", rec.Code, rec.Body.String())
	}
	if body := get(t, srv, "/today").Body.String(); strings.Contains(body, "Nothing recorded today") {
		t.Error("the nudge survived being dismissed")
	}
}

// TestDismissingRefusesWhatItCannotStore.
//
// A dismissal is a row keyed on a kind and a date, read back forever. Anything
// else is a row nothing will ever match again, so it must not be written - and
// must not be answered as though it had been.
func TestDismissingRefusesWhatItCannotStore(t *testing.T) {
	srv := eveningServer(t)

	cases := []struct {
		name string
		path string
		form url.Values
	}{
		{"unknown kind", "/reminders/stop-nagging/dismiss", url.Values{"scope": {"2026-05-13"}}},
		{"scope that is not a date", "/reminders/empty_day/dismiss", url.Values{"scope": {"today"}}},
		{"no scope at all", "/reminders/empty_day/dismiss", url.Values{}},
	}
	for _, c := range cases {
		if code := post(t, srv, c.path, c.form).Code; code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", c.name, code)
		}
	}
	if body := get(t, srv, "/today").Body.String(); !strings.Contains(body, "Nothing recorded today") {
		t.Error("a refused dismissal hid the nudge anyway")
	}
}

// TestRunningTimerNudgeOffersTheStop.
//
// The most valuable of the four - a timer left running overnight costs hours -
// so it carries the control that fixes it rather than a link to a screen.
func TestRunningTimerNudgeOffersTheStop(t *testing.T) {
	srv := eveningServer(t)
	post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}})

	body := get(t, srv, "/today").Body.String()
	// "%d timer is still running." with the count filled in: the plural helper
	// always passes the count, so the singular carries the verb too.
	if !strings.Contains(body, "1 timer is still running") {
		t.Fatal("no nudge about the running timer")
	}
	// The panel's own stop control, not merely the one in the header.
	panel := body[strings.Index(body, "reminder-list"):]
	if end := strings.Index(panel, "</section>"); end > 0 {
		panel = panel[:end]
	}
	if !strings.Contains(panel, `action="/timers/stop-all"`) {
		t.Error("the running-timer nudge does not offer to stop anything")
	}
}

// TestRemindersCanBeSwitchedOff.
//
// Instance-wide, and it has to reach the rendered page rather than only the
// database.
func TestRemindersCanBeSwitchedOff(t *testing.T) {
	srv := eveningServer(t)

	if rec := post(t, srv, "/settings", url.Values{
		"default_currency": {"SEK"}, "default_rounding": {"none"}, "default_rate": {"0"},
		"week_start": {"1"}, "max_timer_hours": {"12"},
		"idle_enabled": {"on"}, "idle_minutes": {"15"},
		// reminders_enabled absent: the checkbox is the switch.
	}); rec.Code >= 400 {
		t.Fatalf("save settings = %d", rec.Code)
	}
	if body := get(t, srv, "/today").Body.String(); strings.Contains(body, "reminder-list") {
		t.Error("the panel is still rendered after reminders were switched off")
	}
}
