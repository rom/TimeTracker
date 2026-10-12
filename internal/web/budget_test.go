package web

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// Budgets over HTTP: setting one, and the report reading it back.
//
// The arithmetic is proved in the domain and the query rules in the service.
// These cover the boundary - a budget typed into a form has to survive the round
// trip, and the report has to say what it cannot say rather than leaving a cell
// blank.

// budgetServer seeds a customer, project and assignment, and returns the server.
func budgetServer(t *testing.T, now time.Time) *Server {
	t.Helper()
	srv, _ := newTestServerAt(t, now)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"},
		"currency": {"SEK"}, "rate": {"1000.00"},
	})
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})
	return srv
}

// setBudget saves the project with the given caps, as the edit form does.
func setBudget(t *testing.T, srv *Server, hours, amount string) {
	t.Helper()
	rec := post(t, srv, "/projects/1", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"},
		"currency": {"SEK"}, "rate": {"1000.00"},
		"budget_hours": {hours}, "budget_amount": {amount},
	})
	if rec.Code >= 400 {
		t.Fatalf("save budget = %d: %s", rec.Code, rec.Body.String())
	}
}

// recordDay adds a day of work, some days before the server's clock.
func recordDay(t *testing.T, srv *Server, daysAgo int, start, end string) {
	t.Helper()
	day := srv.svc.Now().UTC().AddDate(0, 0, -daysAgo).Format("2006-01-02")
	rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {day}, "start": {start}, "end": {end},
	})
	if rec.Code >= 400 {
		t.Fatalf("record entry = %d", rec.Code)
	}
}

// TestBudgetSurvivesTheForm.
//
// Both caps are optional and independent, and the field is text so that blank
// means "no cap" rather than "a cap of nought" - which would report every
// project as an overrun the moment somebody worked on it. The round trip
// matters because the edit form renders the stored value back for editing: a
// budget that saves but does not reappear reads as one that did not save.
func TestBudgetSurvivesTheForm(t *testing.T) {
	srv := budgetServer(t, time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))

	setBudget(t, srv, "37.5", "50000.00")

	form := get(t, srv, "/projects/1/edit").Body.String()
	if !strings.Contains(form, `value="37.5"`) {
		t.Error("the hours budget did not come back in the form")
	}
	if !strings.Contains(form, `value="50000.00"`) {
		t.Error("the money budget did not come back in the form")
	}

	// Clearing both is how a budget is removed, and it has to be possible.
	setBudget(t, srv, "", "")
	if body := get(t, srv, "/reports/budgets").Body.String(); !strings.Contains(body, "No project has a budget") {
		t.Error("clearing both budgets did not remove the project from the report")
	}
}

// TestBudgetFormRefusesWhatItCannotRead.
//
// A budget that silently becomes zero is worse than a rejected form: zero is a
// real value here and means something very different from "unset".
func TestBudgetFormRefusesWhatItCannotRead(t *testing.T) {
	srv := budgetServer(t, time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))

	for _, bad := range []url.Values{
		{"budget_hours": {"quite a lot"}},
		{"budget_amount": {"about fifty thousand"}},
	} {
		form := url.Values{
			"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"},
			"currency": {"SEK"},
		}
		for k, v := range bad {
			form[k] = v
		}
		if code := post(t, srv, "/projects/1", form).Code; code != 400 {
			t.Errorf("%v = %d, want 400", bad, code)
		}
	}
}

// TestBudgetReportShowsConsumptionAndSaysWhatItCannotProject.
//
// The report's two halves. Consumption is arithmetic and is shown; the
// projection is a guess and is shown only when there is enough to guess from -
// otherwise the cell carries the reason, because a blank one reads as "never".
func TestBudgetReportShowsConsumptionAndSaysWhatItCannotProject(t *testing.T) {
	srv := budgetServer(t, time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))
	setBudget(t, srv, "100", "")

	// One day of work: consumption is knowable, a weekly rate is not.
	recordDay(t, srv, 2, "09:00", "17:00")

	body := get(t, srv, "/reports/budgets").Body.String()
	if !strings.Contains(body, "Migration") {
		t.Fatal("the budgeted project is missing from the report")
	}
	if !strings.Contains(body, "8%") {
		t.Errorf("eight of a hundred hours should read as 8%%")
	}
	if !strings.Contains(body, "too little history to estimate") {
		t.Error("a single week of work should refuse to project, and say so")
	}

	// A second active week, and the projection appears.
	recordDay(t, srv, 9, "09:00", "17:00")
	body = get(t, srv, "/reports/budgets").Body.String()
	if strings.Contains(body, "too little history to estimate") {
		t.Error("two active weeks is enough history to estimate from")
	}
	if !strings.Contains(body, "/ week") {
		t.Error("the report does not show a weekly rate")
	}
}

// TestBudgetReportMarksAnOverrun.
//
// The row the screen exists for. The percentage is not capped and the remaining
// figure goes negative, because "12 hours over" is the number somebody needs -
// a row clamped at 100% looks exactly like a project that landed on budget.
func TestBudgetReportMarksAnOverrun(t *testing.T) {
	srv := budgetServer(t, time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))
	setBudget(t, srv, "8", "")

	recordDay(t, srv, 2, "09:00", "17:00")
	recordDay(t, srv, 3, "09:00", "13:00")

	body := get(t, srv, "/reports/budgets").Body.String()
	if !strings.Contains(body, "150%") {
		t.Errorf("twelve hours against an eight-hour budget should read as 150%%")
	}
	if !strings.Contains(body, "1 over budget") {
		t.Error("the heading does not count the overrun")
	}
	if !strings.Contains(body, "already over") {
		t.Error("a project already over should not carry a projection")
	}
	// A real minus sign, which is what the duration formatter emits.
	if !strings.Contains(body, "\u22124h 00m") {
		t.Error("the remaining figure should be negative rather than floored at zero")
	}
}
