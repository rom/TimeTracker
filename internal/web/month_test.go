package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The month overview, the date pickers, and the month headings in the entries
// listing - three ways of answering "where was I in time", which is the question
// the previous/next buttons answer badly.

// TestTheMonthScreenIsACalendarOfTheMonth.
//
// Thirty-one squares for March, each linking to its own day, and the week rows
// linking to the week view. The links are what make it a navigation screen
// rather than a report: somebody opens a month to find the day they are looking
// for.
func TestTheMonthScreenIsACalendarOfTheMonth(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-17"}, "start": {"09:00"},
		"duration": {"2h"}, "billable": {"on"}, "note": {"on the seventeenth"},
	})

	body := get(t, srv, "/month?month=2026-03").Body.String()

	if !strings.Contains(body, "March 2026") {
		t.Errorf("the month is not named on its own screen")
	}
	for _, day := range []string{"2026-03-01", "2026-03-17", "2026-03-31"} {
		if !strings.Contains(body, `href="/today?date=`+day+`"`) {
			t.Errorf("the %s square does not link to its day", day)
		}
	}
	if strings.Contains(body, `href="/today?date=2026-04-01"`) {
		t.Error("a day of April is linked from the March calendar")
	}
	// The hours land on the square: two hours on the 17th, rendered as decimal
	// hours the way every other grid in the application does.
	if !strings.Contains(body, "2.00") {
		t.Errorf("the recorded hours are not on the calendar:\n%s", firstTable(body))
	}
	// The week rows link to the week view, which is where a thin week gets
	// fixed.
	if !strings.Contains(body, `href="/week?date=`) {
		t.Error("the week rows do not link to the week view")
	}
	// And what the month went on.
	if !strings.Contains(body, "Development") {
		t.Error("the month does not say what it went on")
	}
}

// TestTheMonthScreenStepsAndJumps.
//
// Previous and next by month, "this month", and a month input for going
// somewhere directly. The input is the point of the feature: stepping is fine
// for last month and useless for last March.
func TestTheMonthScreenStepsAndJumps(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	body := get(t, srv, "/month?month=2026-03").Body.String()

	for _, expected := range []string{
		`href="/month?month=2026-02"`, `href="/month?month=2026-04"`, `href="/month"`,
		`type="month"`, `name="month"`, `value="2026-03"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("the month navigation is missing %s", expected)
		}
	}

	// The input submits a GET to the same route, so the jump is a URL somebody
	// can bookmark rather than a script.
	if !strings.Contains(body, `<form class="date-jump" method="get" action="/month"`) {
		t.Error("the month jump is not a plain GET form")
	}

	// January steps back to December of the previous year, which is the case an
	// off-by-one in the arithmetic gets wrong.
	january := get(t, srv, "/month?month=2026-01").Body.String()
	if !strings.Contains(january, `href="/month?month=2025-12"`) {
		t.Error("stepping back from January does not reach December")
	}
}

// TestAnUnreadableMonthLandsOnThisMonth.
//
// A stale bookmark, a hand-edited URL, a browser that submits something
// unexpected. None of them should be an error page: the month screen has a
// sensible answer for "I do not know which month you mean".
func TestAnUnreadableMonthLandsOnThisMonth(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	now := srv.svc.Now().UTC()
	for _, query := range []string{"?month=nonsense", "?month=2026-13", "?month=", "?date=nope", ""} {
		rec := get(t, srv, "/month"+query)
		if rec.Code != http.StatusOK {
			t.Errorf("/month%s = %d", query, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), now.Format("2006-01")) {
			t.Errorf("/month%s did not fall back to the current month", query)
		}
	}
}

// TestTheDayAndWeekScreensOfferADatePicker.
//
// The feature asked for, in the two places it was missing: previous, today and
// next are the wrong controls for "the 3rd of last month".
func TestTheDayAndWeekScreensOfferADatePicker(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	for _, screen := range []struct{ path, action string }{
		{"/today?date=2026-03-17", "/today"},
		{"/week?date=2026-03-17", "/week"},
	} {
		body := get(t, srv, screen.path).Body.String()
		if !strings.Contains(body, `method="get" action="`+screen.action+`"`) {
			t.Errorf("%s has no date-jump form", screen.path)
		}
		if !strings.Contains(body, `type="date" name="date"`) {
			t.Errorf("%s has no date input", screen.path)
		}
		// Prefilled with where you are, so the picker opens on the right month
		// rather than on today.
		if !strings.Contains(body, `value="2026-03-17"`) && !strings.Contains(body, `value="2026-03-16"`) {
			t.Errorf("%s does not prefill the date input with the date on screen", screen.path)
		}
		// And a way to zoom out, which is the other half of navigating by date.
		if !strings.Contains(body, `href="/month?date=`) {
			t.Errorf("%s does not offer the month overview", screen.path)
		}
	}

	// The picker actually navigates: a GET with a date lands on that date.
	day := get(t, srv, "/today?date=2026-03-03").Body.String()
	if !strings.Contains(day, "3 March 2026") {
		t.Errorf("the day screen did not go to the date asked for")
	}
	// A week input would be unsupported in half of browsers, so the week screen
	// takes any date inside the week and shows that week.
	week := get(t, srv, "/week?date=2026-03-18").Body.String()
	if !strings.Contains(week, "2026-03-16") {
		t.Error("the week screen did not open the week containing the date")
	}
}

// TestTheEntriesListingMarksMonthBoundaries.
//
// A listing of fifty rows spanning a quarter is a column of fifty dates, which
// the eye slides off. A heading where the month changes gives it the structure
// the dates imply.
func TestTheEntriesListingMarksMonthBoundaries(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	for _, day := range []string{"2026-02-26", "2026-03-02", "2026-03-03", "2026-04-01"} {
		post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {"09:00"},
			"duration": {"1h"}, "billable": {"on"}, "note": {"on " + day},
		})
	}

	body := get(t, srv, "/entries?from=2026-02-01&to=2026-04-30").Body.String()

	for _, month := range []string{"February 2026", "March 2026", "April 2026"} {
		if !strings.Contains(body, month) {
			t.Errorf("the listing has no heading for %s", month)
		}
	}
	if headings := strings.Count(body, `class="month-heading"`); headings != 3 {
		t.Errorf("%d month headings, want 3", headings)
	}
	// The heading carries the figures for what is under it, so nobody has to add
	// the rows up: March has two entries on this page, the others one each.
	if !strings.Contains(body, "2 entries") {
		t.Errorf("the March heading does not say how many rows it covers:\n%s", monthHeadings(body))
	}
	if !strings.Contains(body, "1 entry") {
		t.Errorf("a single-row month is pluralised:\n%s", monthHeadings(body))
	}
}

// TestASingleMonthNeedsNoHeading.
//
// The decoration has to be absent when it would say nothing. A listing entirely
// inside one month, headed "March 2026", repeats the screen's own heading - and
// a marker that appears on every page is one nobody reads.
func TestASingleMonthNeedsNoHeading(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	for _, day := range []string{"2026-03-02", "2026-03-03"} {
		post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {"09:00"},
			"duration": {"1h"}, "billable": {"on"},
		})
	}

	body := get(t, srv, "/entries?from=2026-03-01&to=2026-03-31").Body.String()
	if strings.Contains(body, `class="month-heading"`) {
		t.Error("a listing inside one month carries a month heading")
	}
	// The rows are still there, which is what makes the absence meaningful.
	if !strings.Contains(body, "2026-03-02") && !strings.Contains(body, "02/03/2026") {
		t.Error("the listing is empty, so the assertion above proves nothing")
	}
}

// TestMonthHeadingsAreTranslated.
//
// The month name comes from the catalogue, not from Go's English month names,
// which is the difference between "oktober" and "October" on a Swedish screen.
func TestMonthHeadingsAreTranslated(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	for _, day := range []string{"2026-02-26", "2026-03-02"} {
		post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {"09:00"},
			"duration": {"1h"}, "billable": {"on"},
		})
	}

	swedish := getWithLanguage(t, srv, "/entries?from=2026-02-01&to=2026-03-31",
		"sv-SE,sv;q=0.9").Body.String()
	for _, month := range []string{"februari 2026", "mars 2026"} {
		if !strings.Contains(swedish, month) {
			t.Errorf("the Swedish listing has no heading for %s", month)
		}
	}
	if strings.Contains(swedish, "February 2026") {
		t.Error("the Swedish listing carries an English month name")
	}
	if !strings.Contains(swedish, "poster") && !strings.Contains(swedish, "post") {
		t.Error("the row count beside the heading was not translated")
	}

	// And the month screen's own heading.
	month := getWithLanguage(t, srv, "/month?month=2026-03", "sv-SE,sv;q=0.9").Body.String()
	if !strings.Contains(month, "mars 2026") {
		t.Error("the month screen is not translated")
	}
}

// seedCatalogue creates the customer, project and assignment the screens need.
func seedCatalogue(t *testing.T, srv *Server) {
	t.Helper()

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}, "rate": {"1250"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})
}

// firstTable returns the first table in a page, for a readable failure.
func firstTable(body string) string {
	start := strings.Index(body, "<table")
	if start < 0 {
		return body
	}
	end := strings.Index(body[start:], "</table>")
	if end < 0 {
		end = len(body) - start
	}
	table := body[start : start+end]
	if len(table) > 1500 {
		return table[:1500] + "..."
	}
	return table
}

// monthHeadings returns the heading rows, for a readable failure.
func monthHeadings(body string) string {
	var found []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "month-heading") || strings.Contains(line, "entries") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) > 12 {
		found = found[:12]
	}
	return strings.Join(found, "\n")
}
