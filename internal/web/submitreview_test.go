package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The screen between a week and its submission.
//
// Submitting closes the week to changes and puts it in somebody else's queue.
// The things that would stop it - an empty day, a missing receipt - used to
// arrive as a refusal after the button had been pressed; this screen asks them
// first. What matters at this layer is that the screen and the route agree:
// an offered button that is then refused is worse than no screen at all.

// TestTheWeekOffersAReviewRatherThanAnImmediateSubmit.
//
// The control on the week screen is a link now, not a submit button. The post
// route still works, so anything that already knows it keeps working - a
// bookmark, a script, a second tab.
func TestTheWeekOffersAReviewRatherThanAnImmediateSubmit(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"8h"},
	})

	week := get(t, srv, "/week?date=2026-03-16")
	if week.Code != http.StatusOK {
		t.Fatalf("GET /week = %d", week.Code)
	}
	body := week.Body.String()
	if !strings.Contains(body, "/week/review") {
		t.Error("the week screen does not offer a review before submitting")
	}

	// The direct route is still there.
	if rec := post(t, srv, "/week/submit", url.Values{
		"week": {"2026-03-16"},
	}); rec.Code != http.StatusSeeOther {
		t.Errorf("posting straight to /week/submit = %d, want it to still work", rec.Code)
	}
}

// TestTheReviewScreenSaysWhetherTheWeekIsReady.
//
// Somebody opening it is asking one question. It is answered before anything
// they have to read, and the answer agrees with what the submit route will do.
func TestTheReviewScreenSaysWhetherTheWeekIsReady(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// An empty week: not ready, and it says why.
	empty := get(t, srv, "/week/review?week=2026-03-16")
	if empty.Code != http.StatusOK {
		t.Fatalf("GET the review of an empty week = %d: %s",
			empty.Code, truncate(empty.Body.String()))
	}
	if !strings.Contains(empty.Body.String(), "cannot be submitted") {
		t.Errorf("the review of an empty week does not say it cannot be submitted:\n%s",
			truncate(empty.Body.String()))
	}
	if !strings.Contains(empty.Body.String(), "nothing recorded") {
		t.Error("the review of an empty week does not say what is missing")
	}
	// And offers no submit button, since pressing it would be refused.
	if strings.Contains(empty.Body.String(), `action="/week/submit"`) {
		t.Error("the review of an empty week offers a submit button that would be refused")
	}

	// A full week: ready, with the figure that will be submitted.
	for _, day := range []string{"2026-03-16", "2026-03-17", "2026-03-18", "2026-03-19", "2026-03-20"} {
		if rec := post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {"09:00"}, "duration": {"8h"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("record %s = %d", day, rec.Code)
		}
	}

	full := get(t, srv, "/week/review?week=2026-03-16")
	if full.Code != http.StatusOK {
		t.Fatalf("GET the review of a full week = %d", full.Code)
	}
	ready := full.Body.String()
	if !strings.Contains(ready, "ready to submit") {
		t.Errorf("the review of a full week does not say it is ready:\n%s", truncate(ready))
	}
	if !strings.Contains(ready, "40h 00m") {
		t.Error("the review does not show the figure that will be submitted")
	}
	if !strings.Contains(ready, `action="/week/submit"`) {
		t.Error("the review of a ready week offers no way to submit it")
	}
	// And a way back to change something, since that is the other thing
	// somebody wants from this screen.
	if !strings.Contains(ready, `href="/week?date=2026-03-16"`) {
		t.Error("the review offers no way back to the week")
	}
}

// TestTheReviewShowsTheSameFiguresAsTheWeek.
//
// The grid is one fragment used by both screens. A review whose numbers
// differed from the screen it was reached from would be worse than no review,
// so this compares the rendered grids rather than trusting that they share a
// definition.
func TestTheReviewShowsTheSameFiguresAsTheWeek(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Support"}, "billable": {"on"},
	})

	for _, entry := range []struct{ day, assignment, duration string }{
		{"2026-03-16", "1", "7h30"},
		{"2026-03-17", "2", "4h15"},
		{"2026-03-19", "1", "6h"},
	} {
		if rec := post(t, srv, "/entries", url.Values{
			"assignment_id": {entry.assignment}, "date": {entry.day},
			"start": {"09:00"}, "duration": {entry.duration},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("record %s = %d", entry.day, rec.Code)
		}
	}

	weekGrid := firstTable(get(t, srv, "/week?date=2026-03-16").Body.String())
	reviewGrid := firstTable(get(t, srv, "/week/review?week=2026-03-16").Body.String())

	if weekGrid == "" || reviewGrid == "" {
		t.Fatal("one of the two screens rendered no grid")
	}
	if weekGrid != reviewGrid {
		t.Errorf("the week and its review render different grids;\nweek:\n%s\n\nreview:\n%s",
			truncate(weekGrid), truncate(reviewGrid))
	}
}

// TestTheReviewPointsAtTheExpenseMissingAReceipt.
//
// A customer that requires receipts above an amount requires them before the
// claim is made. The review names the row rather than the week, because "a
// receipt is missing somewhere in these nine costs" is not something somebody
// can act on.
func TestTheReviewPointsAtTheExpenseMissingAReceipt(t *testing.T) {
	srv, _ := newTestServerAt(t, parseDay(t, "2026-03-20"))
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"8h"},
	})

	if rec := post(t, srv, "/terms/customer/1", url.Values{
		"effective_from":         {"2020-01-01"},
		"receipt_required_above": {"50"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save terms = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, "/expenses", url.Values{
		"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"category": {"travel"}, "description": {"Flight to the client"},
		"amount": {"500"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record an expense = %d: %s", rec.Code, rec.Body.String())
	}

	review := get(t, srv, "/week/review?week=2026-03-16")
	if review.Code != http.StatusOK {
		t.Fatalf("GET the review = %d: %s", review.Code, truncate(review.Body.String()))
	}
	body := review.Body.String()

	if !strings.Contains(body, "cannot be submitted") {
		t.Error("a week with an expense missing a required receipt was offered as ready")
	}
	if !strings.Contains(body, "needs a receipt") {
		t.Errorf("the review does not say a receipt is needed:\n%s", truncate(body))
	}
	// The row is named, and marked.
	if !strings.Contains(body, "Flight to the client") {
		t.Error("the review does not list the expense that needs the receipt")
	}
	if !strings.Contains(body, "receipt needed") {
		t.Error("the row is not marked as needing one")
	}
	// No submit button, because pressing it would be refused.
	if strings.Contains(body, `action="/week/submit"`) {
		t.Error("the review offers a submit button for a week that would be refused")
	}

	// Which is the agreement: the route refuses it too.
	if rec := post(t, srv, "/week/submit", url.Values{
		"week": {"2026-03-16"},
	}); rec.Code == http.StatusSeeOther {
		t.Error("the review refused the week and the route accepted it")
	}
}

// TestTheReviewWarnsWithoutBlocking.
//
// An empty Friday is somebody's half-day as often as it is an omission. The
// review says so and still lets them submit, because a review that refused to
// let them past it is a review they learn to click through.
func TestTheReviewWarnsWithoutBlocking(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// Monday and Tuesday only.
	for _, day := range []string{"2026-03-16", "2026-03-17"} {
		post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {day}, "start": {"09:00"}, "duration": {"8h"},
		})
	}

	review := get(t, srv, "/week/review?week=2026-03-16")
	if review.Code != http.StatusOK {
		t.Fatalf("GET the review = %d", review.Code)
	}
	body := review.Body.String()

	if !strings.Contains(body, "ready to submit") {
		t.Error("a week with three empty weekdays was blocked rather than flagged")
	}
	if !strings.Contains(body, "Worth a look") {
		t.Errorf("the review has nothing to say about three empty weekdays:\n%s",
			truncate(body))
	}
	// Named by day, so somebody can go and look at it.
	for _, day := range []string{"Wednesday", "Thursday", "Friday"} {
		if !strings.Contains(body, day) {
			t.Errorf("the review does not name %s as empty", day)
		}
	}
	// And the weekend is not reported as missing.
	for _, day := range []string{"Saturday", "Sunday"} {
		if strings.Count(body, day) > 1 {
			t.Errorf("%s appears more than once, which suggests it was reported "+
				"as an omission as well as being a column heading", day)
		}
	}

	// It submits, which is the point of a warning rather than a blocker.
	if rec := post(t, srv, "/week/submit", url.Values{
		"week": {"2026-03-16"},
	}); rec.Code != http.StatusSeeOther {
		t.Errorf("the review said ready and the submission returned %d", rec.Code)
	}
}

// TestTheReviewOfADecidedWeekOffersTheWayBack.
//
// A stale link, or a reload after submitting. Rather than a button that cannot
// work, the screen says which state the week is in and offers withdrawal.
func TestTheReviewOfADecidedWeekOffersTheWayBack(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"8h"},
	})
	if rec := post(t, srv, "/week/submit", url.Values{
		"week": {"2026-03-16"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("submit = %d", rec.Code)
	}

	review := get(t, srv, "/week/review?week=2026-03-16")
	if review.Code != http.StatusOK {
		t.Fatalf("GET the review of a submitted week = %d", review.Code)
	}
	body := review.Body.String()

	if strings.Contains(body, `action="/week/submit"`) {
		t.Error("the review of a submitted week offers to submit it again")
	}
	if !strings.Contains(body, `action="/week/withdraw"`) {
		t.Error("the review of a submitted week offers no way to take it back")
	}
	if !strings.Contains(body, "already been submitted") {
		t.Errorf("the review does not say the week is already submitted:\n%s",
			truncate(body))
	}
}

// parseDay reads a YYYY-MM-DD date for a fixture's stopped clock.
func parseDay(t *testing.T, day string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatalf("parse %q: %v", day, err)
	}
	return parsed
}
