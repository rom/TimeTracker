package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Recording again what was recorded before: copying a day or a week, switching
// timers, and routines.
//
// All of these write a timesheet from a single click, which is what makes them
// worth testing at this level rather than only in the service. A handler that
// read the wrong day, or that silently did nothing when a field was missing,
// would produce hours against the wrong date from a button somebody pressed
// without reading.

// TestCopyingADayDefaultsToYesterday.
//
// "Copy yesterday" is the button, and the overwhelmingly common case. The
// source being implicit is the whole point of the feature, so the default is
// the behaviour under test rather than an incidental detail.
func TestCopyingADayDefaultsToYesterday(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	const yesterday = "2026-03-16"
	const today = "2026-03-17"

	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {yesterday}, "start": {"09:00"}, "duration": {"3h"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record the source day = %d: %s", rec.Code, rec.Body.String())
	}
	if before := totalOn(t, srv, today); before != noTime {
		t.Fatalf("the target day already totals %q, so this proves nothing", before)
	}

	if rec := post(t, srv, "/days/copy?date="+today, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("copy the day = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, today); got != "3h 00m" {
		t.Errorf("after copying with no source named the target day totals %q, want 3h 00m - "+
			"the default must be the day before", got)
	}

	// The source day is left alone: copying is not moving.
	if got := totalOn(t, srv, yesterday); got != "3h 00m" {
		t.Errorf("the day that was copied from now totals %q, want its original 3h 00m", got)
	}
}

// TestCopyingADayCanNameItsSource.
//
// A day two weeks ago, for somebody who was away and is filling in a week that
// looked like an earlier one.
func TestCopyingADayCanNameItsSource(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	const source = "2026-03-02"
	const target = "2026-03-17"

	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {source}, "start": {"09:00"}, "duration": {"3h"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record the source day = %d", rec.Code)
	}

	if rec := post(t, srv, "/days/copy?date="+target, url.Values{
		"from": {source},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("copy from %s = %d: %s", source, rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, target); got != "3h 00m" {
		t.Errorf("copying from %s left %s totalling %q", source, target, got)
	}

	// An unreadable source date is treated as none given, so the copy falls
	// back to the day before the target - which here has nothing on it, and is
	// refused by name. The refusal naming the fallback day rather than the
	// unreadable text is what tells somebody their typo was ignored.
	rec := post(t, srv, "/days/copy?date="+target, url.Values{
		"from": {"the Tuesday before last"},
	})
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("an unreadable source date = 500, want a refusal")
	}
	if !strings.Contains(rec.Body.String(), "2026-03-16") {
		t.Errorf("the refusal does not name the day it fell back to:\n%s", rec.Body.String())
	}
}

// TestCopyingAWeekDefaultsToTheWeekBefore.
//
// Same shape as the day, seven days back. A week that copied from the day
// before would be a very confusing button.
func TestCopyingAWeekDefaultsToTheWeekBefore(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// Monday and Wednesday of one week.
	for _, day := range []string{"2026-03-09", "2026-03-11"} {
		if rec := post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {"09:00"}, "duration": {"4h"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("record %s = %d", day, rec.Code)
		}
	}

	if rec := post(t, srv, "/weeks/copy?date=2026-03-16", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("copy the week = %d: %s", rec.Code, rec.Body.String())
	}

	// Day for day: the Monday's work lands on the Monday, the Wednesday's on
	// the Wednesday, and the untouched days stay untouched.
	for _, day := range []string{"2026-03-16", "2026-03-18"} {
		if got := totalOn(t, srv, day); got != "4h 00m" {
			t.Errorf("%s totals %q after copying the week before it, want 4h 00m", day, got)
		}
	}
	for _, day := range []string{"2026-03-17", "2026-03-19"} {
		if got := totalOn(t, srv, day); got != noTime {
			t.Errorf("%s totals %q after copying a week that had nothing on that day", day, got)
		}
	}

	// And it can name its source too.
	if rec := post(t, srv, "/weeks/copy?date=2026-03-23", url.Values{
		"from": {"2026-03-09"},
	}); rec.Code != http.StatusSeeOther {
		t.Errorf("copying a named week = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-23"); got != "4h 00m" {
		t.Errorf("copying from a named week left its Monday totalling %q, want 4h 00m", got)
	}
}

// TestSwitchingTimersStopsTheOldOneFirst.
//
// One click when somebody moves to another task. If it started the new timer
// without stopping the old one, two timers would run and the day would total
// more hours than the day has.
func TestSwitchingTimersStopsTheOldOneFirst(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Support"}, "billable": {"on"},
	})

	if rec := post(t, srv, "/timers/start", url.Values{
		"assignment_id": {"1"}, "note": {"First thing"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := post(t, srv, "/timers/switch", url.Values{
		"assignment_id": {"2"}, "note": {"Second thing"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("switch = %d: %s", rec.Code, rec.Body.String())
	}

	if body := get(t, srv, "/today").Body.String(); !strings.Contains(body, "Second thing") {
		t.Error("the new timer is not running after a switch")
	}
	if running := runningTimers(t, srv); len(running) != 1 {
		t.Errorf("%d timers running after a switch (%v), want 1 - a switch that "+
			"left the old one going would total more hours than the day has",
			len(running), running)
	}
}

// TestStoppingTimers.
//
// Stopping is idempotent, which is the assertion that matters: a double click
// or two open tabs must not produce a doubled duration, and the second stop
// must not be an error on somebody's screen either.
func TestStoppingTimers(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Support"}, "billable": {"on"},
	})

	if rec := post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("start = %d", rec.Code)
	}
	if rec := post(t, srv, "/timers/1/stop", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("stop = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, "/timers/1/stop", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Errorf("stopping an already-stopped timer = %d, want it to be a no-op", rec.Code)
	}

	if running := runningTimers(t, srv); len(running) != 0 {
		t.Errorf("timers %v are still running after stopping", running)
	}

	// Stopping something that was never started is not found rather than a
	// server error: it is a stale button in an old tab, not a bug.
	if rec := post(t, srv, "/timers/999/stop", url.Values{}); rec.Code == http.StatusInternalServerError {
		t.Errorf("stopping a nonexistent timer = 500, want a refusal")
	}

	// Stop-all is the end of the day, and is also idempotent.
	for _, assignment := range []string{"1", "2"} {
		if rec := post(t, srv, "/timers/start", url.Values{
			"assignment_id": {assignment},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("start on %s = %d", assignment, rec.Code)
		}
	}
	if rec := post(t, srv, "/timers/stop-all", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("stop all = %d: %s", rec.Code, rec.Body.String())
	}
	if running := runningTimers(t, srv); len(running) != 0 {
		t.Errorf("timers %v are still running after stopping everything", running)
	}
	if rec := post(t, srv, "/timers/stop-all", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Errorf("stopping everything twice = %d, want a no-op", rec.Code)
	}
}

// TestDraggingAnEntryMovesTimeAndNothingElse.
//
// The timeline's drag, resize and keyboard nudges all post here. The thing to
// protect is what it does *not* change: a drag moves time, and must not quietly
// change what the time was for. Reusing the full edit input here would let a
// stale form field do exactly that.
func TestDraggingAnEntryMovesTimeAndNothingElse(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Support"}, "billable": {"on"},
	})

	const day = "2026-03-16"
	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {day}, "start": {"09:00"}, "duration": {"2h"},
		"note": {"Careful work"}, "tags": {"onsite"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record = %d: %s", rec.Code, rec.Body.String())
	}

	// A drag to 11:00, resized to three hours, with a stale assignment field
	// alongside it - exactly what an old form in another tab would send.
	if rec := post(t, srv, "/entries/1/move", url.Values{
		"start": {"11:00"}, "duration": {"3h"},
		"assignment_id": {"2"}, "note": {"Rewritten"}, "billable": {""},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("move = %d: %s", rec.Code, rec.Body.String())
	}

	edit := get(t, srv, "/entries/1/edit").Body.String()
	for _, kept := range []string{"Careful work", "onsite"} {
		if !strings.Contains(edit, kept) {
			t.Errorf("a drag changed the entry's %q - it must only move time", kept)
		}
	}
	if !strings.Contains(edit, `value="11:00"`) {
		t.Error("the drag did not move the entry's start time")
	}
	if !strings.Contains(edit, "Development") {
		t.Error("a drag moved the entry to another assignment")
	}

	// A drag onto another day carries the date.
	if rec := post(t, srv, "/entries/1/move", url.Values{
		"date": {"2026-03-17"}, "start": {"09:00"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("move across days = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-17"); got == noTime {
		t.Error("dragging an entry onto the next day left it on the old one")
	}

	// An unreadable time is the caller's mistake, reported rather than crashed
	// on, and leaves the entry alone.
	for _, bad := range []url.Values{
		{"start": {"half past nine"}},
		{"start": {"09:00"}, "duration": {"ages"}},
	} {
		rec := post(t, srv, "/entries/1/move", bad)
		if rec.Code == http.StatusInternalServerError {
			t.Errorf("moving with %v = 500, want a refusal", bad)
		}
		if rec.Code == http.StatusSeeOther {
			t.Errorf("moving with %v was accepted", bad)
		}
	}

	// And moving an entry that does not exist is not found.
	if rec := post(t, srv, "/entries/999/move", url.Values{
		"start": {"09:00"},
	}); rec.Code == http.StatusInternalServerError {
		t.Error("moving a nonexistent entry = 500, want a refusal")
	}
}

// TestRoutinesAreSavedAppliedAndDeleted.
//
// A routine is a template somebody fills a day from, so the whole round trip
// matters: the weekdays and tags arrive as awkward form values, and a routine
// saved with the wrong days fills the wrong days.
func TestRoutinesAreSavedAppliedAndDeleted(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	save := url.Values{
		"assignment_id": {"1"}, "name": {"Standup"}, "note": {"Daily standup"},
		"weekdays": {"1", "2", "3", "4", "5"}, "start": {"09:00"},
		"duration": {"15m"}, "billable": {"on"}, "active": {"on"},
		"tags": {"meeting"},
	}
	if rec := post(t, srv, "/routines", save); rec.Code != http.StatusSeeOther {
		t.Fatalf("save routine = %d: %s", rec.Code, rec.Body.String())
	}

	screen := get(t, srv, "/routines").Body.String()
	if !strings.Contains(screen, "Standup") {
		t.Fatal("the saved routine is not on the routines screen")
	}

	// The edit form comes back filled in, which is what makes a routine
	// editable rather than only replaceable.
	editing := get(t, srv, "/routines?edit=1").Body.String()
	if !strings.Contains(editing, "Daily standup") {
		t.Error("the edit form does not carry the routine's note")
	}

	// Applying it records the entry on a chosen day.
	const monday = "2026-03-16"
	if rec := post(t, srv, "/routines/1/apply?date="+monday, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply routine = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, monday); got != "15m" {
		t.Errorf("applying a 15-minute routine left the day totalling %q", got)
	}

	// Apply-all is the morning button, and is the same work for every routine
	// due that day.
	if rec := post(t, srv, "/routines/apply-all?date=2026-03-17", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply all = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-17"); got != "15m" {
		t.Errorf("applying every routine due on a Tuesday left the day totalling %q", got)
	}

	// A Sunday is not in the routine's weekdays, so apply-all records nothing
	// rather than everything.
	if rec := post(t, srv, "/routines/apply-all?date=2026-03-22", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply all on a Sunday = %d", rec.Code)
	}
	if got := totalOn(t, srv, "2026-03-22"); got != noTime {
		t.Errorf("a weekday routine was applied on a Sunday, leaving it at %q", got)
	}

	// An unreadable length is the person's typo, named in the refusal.
	broken := url.Values{}
	for key, values := range save {
		broken[key] = values
	}
	broken.Set("duration", "a bit")
	rec := post(t, srv, "/routines", broken)
	if rec.Code == http.StatusSeeOther {
		t.Error("a routine with an unreadable length was saved")
	}
	if !strings.Contains(rec.Body.String(), "30m") {
		t.Errorf("the refusal does not say what a length looks like:\n%s", rec.Body.String())
	}

	// So is an unknown kind of time.
	broken.Set("duration", "15m")
	broken.Set("kind", "sleeping")
	if rec := post(t, srv, "/routines", broken); rec.Code == http.StatusSeeOther {
		t.Error("a routine with an unknown kind of time was saved")
	}

	if rec := post(t, srv, "/routines/1/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete routine = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(get(t, srv, "/routines").Body.String(), "Standup") {
		t.Error("the deleted routine is still on the screen")
	}
}

// TestTheseWritesRedrawTheScreenForAScriptedClient.
//
// Every one of these mutations affects several regions at once - the timer
// header, the day list and both totals - so the handler asks the page to redraw
// rather than swapping one fragment. A partial update that missed a region
// would show stale numbers, which is worse than a redraw.
//
// Asserted once, for the whole family, because it is the same two lines in
// every handler and a new one that forgets them is the thing to catch.
func TestTheseWritesRedrawTheScreenForAScriptedClient(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	const day = "2026-03-16"
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {day}, "start": {"09:00"}, "duration": {"2h"},
	})
	post(t, srv, "/routines", url.Values{
		"assignment_id": {"1"}, "name": {"Standup"}, "weekdays": {"1"},
		"duration": {"15m"}, "active": {"on"},
	})

	writes := []struct {
		name string
		path string
		form url.Values
	}{
		{"start a timer", "/timers/start", url.Values{"assignment_id": {"1"}}},
		{"switch timers", "/timers/switch", url.Values{"assignment_id": {"1"}}},
		{"stop all timers", "/timers/stop-all", nil},
		{"copy a day", "/days/copy?date=2026-03-17", nil},
		{"copy a week", "/weeks/copy?date=2026-03-23", url.Values{"from": {day}}},
		{"apply a routine", "/routines/1/apply?date=" + day, nil},
		{"apply every routine", "/routines/apply-all?date=" + day, nil},
		{"move an entry", "/entries/1/move", url.Values{"start": {"10:00"}}},
	}

	for _, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			form := write.form
			if form == nil {
				form = url.Values{}
			}
			rec := postHTMX(srv, write.path, form)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("%s from a scripted client = %d, want 204\n%s",
					write.name, rec.Code, rec.Body.String())
			}
			if rec.Header().Get("HX-Refresh") != "true" {
				t.Errorf("%s did not ask the page to redraw, so its totals would go stale",
					write.name)
			}
		})
	}
}

// noTime is what the totals block shows for a day with nothing on it.
const noTime = "0s"

// runningTimers is the set of timer ids the day screen offers a stop button
// for.
//
// Counting the ids rather than the buttons: a running timer is offered a stop
// in the header and again in the day list, each as both a form action and an
// hx-post, so one timer accounts for four occurrences.
func runningTimers(t *testing.T, srv *Server) []string {
	t.Helper()
	rec := get(t, srv, "/today")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /today = %d", rec.Code)
	}
	var ids []string
	seen := map[string]bool{}
	for _, match := range stopButton.FindAllStringSubmatch(rec.Body.String(), -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			ids = append(ids, match[1])
		}
	}
	return ids
}

var stopButton = regexp.MustCompile(`/timers/(\d+)/stop`)

// totalOn reads the first figure out of the day screen's totals block.
//
// Reading the rendered total rather than counting assignment names in the page:
// the entry form carries a dropdown of every assignment, so a page for an empty
// day mentions them all. The total is the figure a person actually reads.
func totalOn(t *testing.T, srv *Server, date string) string {
	t.Helper()
	rec := get(t, srv, "/today?date="+date)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET today for %s = %d", date, rec.Code)
	}
	const marker = `class="total-value">`
	body := rec.Body.String()
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("the day screen for %s shows no totals", date)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, "<")
	if end < 0 {
		t.Fatalf("malformed totals block on the day screen for %s", date)
	}
	return rest[:end]
}

// postHTMX issues a form POST announcing itself as a scripted client.
func postHTMX(srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
