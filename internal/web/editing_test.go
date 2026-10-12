package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Editing the catalogue, and the expenses screen with things attached to it.
//
// An edit form posts every field it shows, so a handler that forgot one would
// blank it on every save - the kind of defect that shows up weeks later as "the
// rate keeps resetting". These walk each editor's round trip, and the expenses
// screen with receipts on it, which is the only shape that exercises the
// per-row work it does.

// TestEditingAnAssignmentKeepsWhatTheFormDoesNotShow.
//
// The handler reads the existing row and overlays the form onto it, rather than
// building a new one from the fields. That is what lets the form omit a field
// without erasing it - and it is also what makes a missing checkbox mean
// "unticked" rather than "unchanged", which is the one case worth being
// explicit about.
func TestEditingAnAssignmentKeepsWhatTheFormDoesNotShow(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Rollout"}, "billable": {"on"},
	})

	if rec := get(t, srv, "/assignments/1/edit"); rec.Code != http.StatusOK {
		t.Fatalf("GET the edit form = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := post(t, srv, "/assignments/1", url.Values{
		"name": {"Development (phase 2)"}, "code": {"DEV2"},
		"colour_key": {"blue"}, "icon": {"wrench"},
		"rate": {"150"}, "currency": {"SEK"},
		"billable": {"on"}, "favourite": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}

	form := get(t, srv, "/assignments/1/edit").Body.String()
	for field, want := range map[string]string{
		"name": "Development (phase 2)", "code": "DEV2", "rate": "150.00",
	} {
		if !fieldHasValue(form, field, want) {
			t.Errorf("after the edit the form's %s is not %q", field, want)
		}
	}
	for _, checked := range []string{"billable", "favourite"} {
		if !checkboxIsTicked(form, checked) {
			t.Errorf("%s came back unticked after being ticked", checked)
		}
	}

	// Unticking is the absence of the field, which is how a browser submits an
	// unchecked box. It has to mean unticked, not unchanged.
	if rec := post(t, srv, "/assignments/1", url.Values{
		"name": {"Development (phase 2)"}, "rate": {"150"}, "currency": {"SEK"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("untick = %d: %s", rec.Code, rec.Body.String())
	}
	form = get(t, srv, "/assignments/1/edit").Body.String()
	for _, unchecked := range []string{"billable", "favourite"} {
		if checkboxIsTicked(form, unchecked) {
			t.Errorf("%s is still ticked after being submitted unticked", unchecked)
		}
	}

	// Moving it to another project carries its history with it rather than
	// leaving the entries orphaned.
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"2h"},
	})
	if rec := post(t, srv, "/assignments/1", url.Values{
		"name": {"Development (phase 2)"}, "project_id": {"2"},
		"rate": {"150"}, "currency": {"SEK"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("move to another project = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-16"); got != "2h 00m" {
		t.Errorf("the day totals %q after the assignment moved project, want 2h 00m", got)
	}

	// An unreadable rate is a typo, not a rate of zero.
	rec := post(t, srv, "/assignments/1", url.Values{
		"name": {"Development"}, "rate": {"a hundred and fifty"}, "currency": {"SEK"},
	})
	if rec.Code == http.StatusSeeOther {
		t.Error("an assignment was saved with an unreadable rate")
	}
	if form := get(t, srv, "/assignments/1/edit").Body.String(); !fieldHasValue(form, "rate", "150.00") {
		t.Error("a refused edit changed the rate anyway")
	}

	// Editing something that is not there is not found, which is a stale link
	// rather than a fault.
	if rec := post(t, srv, "/assignments/999", url.Values{
		"name": {"Nothing"},
	}); rec.Code != http.StatusNotFound {
		t.Errorf("editing a nonexistent assignment = %d, want 404", rec.Code)
	}
	if rec := get(t, srv, "/assignments/999/edit"); rec.Code != http.StatusNotFound {
		t.Errorf("the edit form for a nonexistent assignment = %d, want 404", rec.Code)
	}
}

// checkboxIsTicked reports whether a checkbox of that name is checked.
func checkboxIsTicked(body, field string) bool {
	marker := `name="` + field + `"`
	for _, chunk := range strings.Split(body, marker)[1:] {
		tag := chunk
		if end := strings.Index(tag, ">"); end >= 0 {
			tag = tag[:end]
		}
		if strings.Contains(tag, "checked") {
			return true
		}
	}
	return false
}

// TestEditingACustomerAndAProject.
//
// The same shape one level up. The customer's currency is the one every amount
// under it is typed and shown in, so an edit that dropped it would silently
// change what a number on an invoice means.
func TestEditingACustomerAndAProject(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	if rec := post(t, srv, "/customers/1", url.Values{
		"name": {"Acme Corporation"}, "currency": {"EUR"}, "rate": {"140"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save customer = %d: %s", rec.Code, rec.Body.String())
	}
	customer := get(t, srv, "/customers/1/edit").Body.String()
	if !fieldHasValue(customer, "name", "Acme Corporation") {
		t.Error("the customer's new name did not come back")
	}
	if !fieldHasValue(customer, "currency", "EUR") {
		t.Error("the customer's currency did not come back - every amount under " +
			"it is typed and shown in that currency")
	}

	if rec := post(t, srv, "/projects/1", url.Values{
		"customer_id": {"1"}, "name": {"Migration (phase 2)"},
		"rate": {"160"}, "currency": {"EUR"}, "billable": {"on"},
		"budget_hours": {"200"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save project = %d: %s", rec.Code, rec.Body.String())
	}
	project := get(t, srv, "/projects/1/edit").Body.String()
	if !fieldHasValue(project, "name", "Migration (phase 2)") {
		t.Error("the project's new name did not come back")
	}
	if !fieldHasValue(project, "rate", "160.00") {
		t.Error("the project's rate did not come back")
	}

	// A budget makes the project appear on the burn report, which is the only
	// place a budget is visible at all.
	if !strings.Contains(get(t, srv, "/reports/budgets").Body.String(), "Migration (phase 2)") {
		t.Error("a project with a budget is not on the budget report")
	}

	// Archiving takes it off the forms without deleting its history.
	if rec := post(t, srv, "/projects/1/archive", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("archive = %d: %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{"/customers/999/edit", "/projects/999/edit"} {
		if rec := get(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// TestFavouritingAnAssignmentIsOneClick.
//
// Reshaping the working set is something people do constantly, so it is a
// button on the day screen rather than a field in an edit form. It toggles,
// because the same button has to put it back.
func TestFavouritingAnAssignmentIsOneClick(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	ticked := func() bool {
		return checkboxIsTicked(get(t, srv, "/assignments/1/edit").Body.String(), "favourite")
	}
	if ticked() {
		t.Fatal("a new assignment is already a favourite, so this proves nothing")
	}

	if rec := post(t, srv, "/assignments/1/favourite", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("favourite = %d: %s", rec.Code, rec.Body.String())
	}
	if !ticked() {
		t.Error("the assignment is not a favourite after being favourited")
	}

	if rec := post(t, srv, "/assignments/1/favourite", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("unfavourite = %d: %s", rec.Code, rec.Body.String())
	}
	if ticked() {
		t.Error("the button does not put it back")
	}

	if rec := post(t, srv, "/assignments/999/favourite", url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("favouriting something that is not there = %d, want 404", rec.Code)
	}
}

// TestTheExpensesScreenShowsWhatIsAttachedToEachClaim.
//
// A row shows what is attached rather than only how many, and marks the claims
// missing evidence their customer's contract requires. Both are resolved per
// row, so the screen only does that work when there is something to do - which
// means an expenses screen with no receipts on it exercises none of it, and
// this is the shape that does.
func TestTheExpensesScreenShowsWhatIsAttachedToEachClaim(t *testing.T) {
	// A stopped clock, because the screen defaults to the last three months and
	// a test pinned to a fixed date would quietly stop covering anything once
	// that date fell out of the window.
	srv, _ := newTestServerAt(t, time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC))
	seedCatalogue(t, srv)

	// A contract that requires a receipt above a small amount.
	if rec := post(t, srv, "/terms/customer/1", url.Values{
		"effective_from":         {"2020-01-01"},
		"receipt_required_above": {"50"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save terms = %d: %s", rec.Code, rec.Body.String())
	}

	// A small claim, under the threshold, and a large one over it.
	for _, amount := range []string{"10", "500"} {
		if rec := post(t, srv, "/expenses", url.Values{
			"project_id": {"1"}, "spent_on": {"2026-03-16"},
			"category": {"travel"}, "description": {"Taxi for " + amount},
			"amount": {amount}, "currency": {"SEK"}, "billable": {"on"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("record an expense of %s = %d: %s", amount, rec.Code, rec.Body.String())
		}
	}

	screen := get(t, srv, "/expenses")
	if screen.Code != http.StatusOK {
		t.Fatalf("GET /expenses = %d: %s", screen.Code, screen.Body.String())
	}
	for _, shown := range []string{"Taxi for 10", "Taxi for 500"} {
		if !strings.Contains(screen.Body.String(), shown) {
			t.Errorf("the screen does not show %q", shown)
		}
	}

	// Attach a receipt to the large one, and the screen names the file rather
	// than only counting it.
	if rec := upload(t, srv, "expense", 2, "receipt.txt",
		[]byte("Taxi receipt, 500 SEK")); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload a receipt = %d: %s", rec.Code, rec.Body.String())
	}

	withReceipt := get(t, srv, "/expenses")
	if withReceipt.Code != http.StatusOK {
		t.Fatalf("GET /expenses with a receipt = %d: %s",
			withReceipt.Code, truncate(withReceipt.Body.String()))
	}
	if !strings.Contains(withReceipt.Body.String(), "receipt.txt") {
		t.Error("the screen does not name the attached receipt")
	}

	// And the filters narrow it, which is the other half of this screen.
	filtered := get(t, srv, "/expenses?project=1&from=2026-03-01&to=2026-03-31")
	if filtered.Code != http.StatusOK {
		t.Fatalf("a filtered listing = %d", filtered.Code)
	}
	if !strings.Contains(filtered.Body.String(), "Taxi for 500") {
		t.Error("a filter that should match everything hid a claim")
	}
	elsewhere := get(t, srv, "/expenses?from=2027-01-01&to=2027-12-31")
	if strings.Contains(elsewhere.Body.String(), "Taxi for 500") {
		t.Error("a claim appeared outside the range it was filtered to")
	}
}
