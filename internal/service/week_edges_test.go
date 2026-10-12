package service

import (
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// The week view's edges, and the catalogue matching the importer does.

// TestAWeekAcrossADaylightSavingShift.
//
// The week grid places each entry by the number of hours between the week's
// start and the entry, divided by 24. In a zone that changes its clocks, one
// of those days is 23 or 25 hours long, so the arithmetic can land just
// outside the seven days - and the entry has to be clamped into the week
// rather than dropped, because an hour missing from a timesheet is an hour
// nobody bills.
func TestAWeekAcrossADaylightSavingShift(t *testing.T) {
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}

	f := newFixture(t)
	// The user's own zone is what the grid is built in.
	user, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := f.svc.SetTimeZone(f.ctx, "Europe/Stockholm"); err != nil {
		t.Fatalf("set the zone: %v", err)
	}
	user.TimeZone = "Europe/Stockholm"
	f.user = user
	f.ctx = auth.WithUser(f.ctx, user)

	// The spring-forward weekend: 29 March 2026 at 02:00 local, which makes
	// that Sunday 23 hours long.
	monday := time.Date(2026, 3, 23, 0, 0, 0, 0, stockholm)
	var recorded int
	for day := 0; day < 7; day++ {
		start := monday.AddDate(0, 0, day).Add(9 * time.Hour)
		end := start.Add(time.Hour)
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: start, EndedAt: &end,
			Billable: true,
		}); err != nil {
			t.Fatalf("record day %d: %v", day, err)
		}
		recorded++
	}

	view, err := f.svc.Week(f.ctx, monday.Add(36*time.Hour))
	if err != nil {
		t.Fatalf("week: %v", err)
	}

	// Every hour recorded is in the grid: seven days, one hour each.
	if view.Totals.SummedSeconds != int64(recorded)*3600 {
		t.Errorf("the week totals %ds, want %d hours - an hour that fell outside "+
			"the arithmetic is an hour nobody bills",
			view.Totals.SummedSeconds, recorded)
	}

	// And each lands on a day of the week rather than being piled on one.
	days := 0
	for _, row := range view.Rows {
		for _, seconds := range row.Seconds {
			if seconds > 0 {
				days++
			}
		}
	}
	if days != 7 {
		t.Errorf("the week's hours fall on %d days, want 7", days)
	}

	// The autumn shift, where a day is 25 hours long, is the other direction.
	autumn := time.Date(2026, 10, 19, 0, 0, 0, 0, stockholm)
	for day := 0; day < 7; day++ {
		start := autumn.AddDate(0, 0, day).Add(9 * time.Hour)
		end := start.Add(time.Hour)
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: start, EndedAt: &end,
			Billable: true,
		}); err != nil {
			t.Fatalf("record autumn day %d: %v", day, err)
		}
	}
	autumnView, err := f.svc.Week(f.ctx, autumn.Add(36*time.Hour))
	if err != nil {
		t.Fatalf("autumn week: %v", err)
	}
	if autumnView.Totals.SummedSeconds != 7*3600 {
		t.Errorf("the autumn week totals %ds, want seven hours",
			autumnView.Totals.SummedSeconds)
	}
}

// TestTheWeekGridIsOrderedForAReader.
//
// Rows are grouped by customer and then by what they are, so a week with four
// engagements on it reads the same way every time. A grid that reordered
// itself between page loads is one nobody can scan.
func TestTheWeekGridIsOrderedForAReader(t *testing.T) {
	f := newFixture(t)

	// Two more customers, named so that their alphabetical order is not the
	// order they are created in.
	for _, name := range []string{"Zenith", "Bluebird"} {
		customer, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
			Name: name, Currency: "EUR",
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		project, err := f.svc.CreateProject(f.ctx, domain.Project{
			CustomerID: customer.ID, Name: name + " work", BillableDefault: true,
		})
		if err != nil {
			t.Fatalf("create a project for %s: %v", name, err)
		}
		assignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
			ProjectID: project.ID, Name: "Development", BillableDefault: true,
		})
		if err != nil {
			t.Fatalf("create an assignment for %s: %v", name, err)
		}
		end := at(f.now, 10, 0)
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: assignment.ID, StartedAt: at(f.now, 9, 0), EndedAt: &end,
			Billable: true,
		}); err != nil {
			t.Fatalf("record for %s: %v", name, err)
		}
	}
	mustCreate(t, f, at(f.now, 11, 0), 3600)

	view, err := f.svc.Week(f.ctx, f.now)
	if err != nil {
		t.Fatalf("week: %v", err)
	}
	if len(view.Rows) != 3 {
		t.Fatalf("%d rows, want one per engagement", len(view.Rows))
	}

	var customers []string
	for _, row := range view.Rows {
		customers = append(customers, row.Assignment.CustomerName)
	}
	for i := 1; i < len(customers); i++ {
		if customers[i] < customers[i-1] {
			t.Errorf("the rows are not grouped by customer: %v", customers)
			break
		}
	}

	// The same week read twice is the same order, which is the property a
	// reader actually depends on.
	again, err := f.svc.Week(f.ctx, f.now)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	for i := range again.Rows {
		if again.Rows[i].Assignment.ID != view.Rows[i].Assignment.ID {
			t.Errorf("the grid reordered itself between two reads at row %d", i)
			break
		}
	}
}

// TestTheImporterMatchesNamesItAlreadyHas.
//
// Importing into an existing catalogue has to find what is there, whatever
// case the exporting system used, or every row creates a near-duplicate
// customer and the catalogue doubles.
func TestTheImporterMatchesNamesItAlreadyHas(t *testing.T) {
	f := newFixture(t)

	before, err := f.svc.Customers(f.ctx, true)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}

	// Every row names the existing catalogue in a different case, and asks
	// for creation if needed - which must not be needed.
	const csv = "date,hours,customer,project,assignment\n" +
		"2026-03-16,1,ACME,MIGRATION,DEVELOPMENT\n" +
		"2026-03-17,1,acme,migration,development\n" +
		"2026-03-18,1,Acme,Migration,Development\n" +
		"2026-03-19,1,aCmE,mIgRaTiOn,dEvElOpMeNt\n"

	imported, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(csv), true)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 4 {
		t.Errorf("imported %d rows, want 4", imported)
	}

	after, err := f.svc.Customers(f.ctx, true)
	if err != nil {
		t.Fatalf("re-list customers: %v", err)
	}
	if len(after) != len(before) {
		var names []string
		for _, customer := range after {
			names = append(names, customer.Name)
		}
		t.Errorf("the catalogue went from %d customers to %d (%v); four spellings "+
			"of one name must match the one that is there",
			len(before), len(after), names)
	}

	assignments, err := f.svc.Assignments(f.ctx, 0, true)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	if len(assignments) != 1 {
		t.Errorf("%d assignments after the import, want the 1 that was there",
			len(assignments))
	}

	// A genuinely new name is created, so the matching is not simply refusing
	// to make anything.
	const other = "date,hours,customer,project,assignment\n" +
		"2026-03-20,1,Globex,Rollout,Support\n"
	if _, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(other), true); err != nil {
		t.Fatalf("import a new name: %v", err)
	}
	grown, err := f.svc.Customers(f.ctx, true)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if len(grown) != len(before)+1 {
		t.Errorf("%d customers after importing a new one, want %d",
			len(grown), len(before)+1)
	}
}
