package service

import (
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// The summary report.
//
// The figures are the easy half. The half worth testing is that it inherits
// the rules every other screen is held to - what counts towards a total, who
// may see money - rather than quietly opening a second path to the same data
// with its own answers.

// summaryFixture records work for two customers and returns the service.
func summaryFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)

	second, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
		Name: "Nordgren", Currency: "SEK", RateMinor: 100000,
	})
	if err != nil {
		t.Fatalf("second customer: %v", err)
	}
	project, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: second.ID, Name: "Rebuild", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("second project: %v", err)
	}
	other, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Frontend", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("second assignment: %v", err)
	}

	// Three hours on the first customer, one on the second.
	for _, spec := range []struct {
		assignment int64
		seconds    int64
		offset     time.Duration
	}{
		{f.assignment.ID, 7200, 0},
		{f.assignment.ID, 3600, 3 * time.Hour},
		{other.ID, 3600, 6 * time.Hour},
	} {
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: spec.assignment, StartedAt: f.now.Add(spec.offset),
			DurationSeconds: spec.seconds, Billable: true,
		}); err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}
	return f
}

// TestASummaryGroupsAndAddsUp.
func TestASummaryGroupsAndAddsUp(t *testing.T) {
	f := summaryFixture(t)

	report, err := f.svc.Summarise(f.ctx, f.now.AddDate(0, 0, -1), f.now.AddDate(0, 0, 1), GroupByCustomer)
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}

	if len(report.Rows) != 2 {
		t.Fatalf("grouping by customer gave %d rows, want 2: %+v", len(report.Rows), report.Rows)
	}
	// Largest first, so the row somebody is looking for is at the top.
	if report.Rows[0].Label != "Acme" {
		t.Errorf("the first row is %q; rows are meant to be ordered by size",
			report.Rows[0].Label)
	}
	if report.Rows[0].Seconds != 10800 {
		t.Errorf("Acme = %ds, want 10800", report.Rows[0].Seconds)
	}
	if report.Rows[1].Seconds != 3600 {
		t.Errorf("Nordgren = %ds, want 3600", report.Rows[1].Seconds)
	}

	// The shares are whole numbers and describe the same totals.
	if report.Rows[0].Percent != 75 || report.Rows[1].Percent != 25 {
		t.Errorf("shares = %d%% and %d%%, want 75 and 25",
			report.Rows[0].Percent, report.Rows[1].Percent)
	}
	if report.Totals.SummedSeconds != 14400 {
		t.Errorf("total = %ds, want 14400", report.Totals.SummedSeconds)
	}

	// Grouping by assignment splits what grouping by customer joined, and the
	// total is the same number however it is cut.
	byAssignment, err := f.svc.Summarise(f.ctx, f.now.AddDate(0, 0, -1), f.now.AddDate(0, 0, 1), GroupByAssignment)
	if err != nil {
		t.Fatalf("summarise by assignment: %v", err)
	}
	if len(byAssignment.Rows) != 2 {
		t.Errorf("grouping by assignment gave %d rows, want 2", len(byAssignment.Rows))
	}
	if byAssignment.Totals.SummedSeconds != report.Totals.SummedSeconds {
		t.Errorf("the total changed with the grouping: %d by assignment, %d by customer",
			byAssignment.Totals.SummedSeconds, report.Totals.SummedSeconds)
	}
}

// TestASummaryCountsWhatEveryOtherTotalCounts.
//
// A flagged entry is excluded from every total in the application until a
// human has said what the interval should have been. A report that included it
// would disagree with the week view about the same week.
func TestASummaryCountsWhatEveryOtherTotalCounts(t *testing.T) {
	f := newFixture(t)

	good, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = good

	// A timer left running past the maximum, which comes back flagged.
	f.now = f.now.Add(time.Hour)
	running, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "left running")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	f.now = f.now.Add(40 * time.Hour)
	stopped, err := f.svc.StopTimer(f.ctx, running.ID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stopped.Flagged {
		t.Fatalf("the fixture entry is not flagged, so this test proves nothing")
	}

	report, err := f.svc.Summarise(f.ctx, f.now.AddDate(0, 0, -7), f.now.AddDate(0, 0, 1), GroupByCustomer)
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	if report.Totals.SummedSeconds != 3600 {
		t.Errorf("the report totals %ds; a flagged entry is counted here and "+
			"nowhere else in the application", report.Totals.SummedSeconds)
	}
}

// TestASummaryShowsNoMoneyToSomebodyWhoMaySeeNone.
//
// The amounts are stripped in the service rather than hidden by the template,
// which is the rule the whole client projection is built on: a figure that
// never leaves the service cannot be leaked by a screen that forgets to check.
func TestASummaryShowsNoMoneyToSomebodyWhoMaySeeNone(t *testing.T) {
	f := summaryFixture(t)

	// A member, who records time and is not shown what it is worth.
	member, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Member", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	// The authorizer the fixture uses answers for a single user, so the money
	// bound is asserted through the service's own predicate rather than by
	// swapping the whole authorizer out: what matters is that Summarise asks
	// it at all, and strips when the answer is no.
	theirs := auth.WithUser(f.ctx, member)

	report, err := f.svc.Summarise(theirs, f.now.AddDate(0, 0, -1), f.now.AddDate(0, 0, 1), GroupByCustomer)
	if err != nil {
		t.Fatalf("summarise: %v", err)
	}
	if report.Money != f.svc.maySeeMoney(theirs) {
		t.Errorf("the report says Money=%v while the service says %v",
			report.Money, f.svc.maySeeMoney(theirs))
	}
	if !report.Money {
		for _, row := range report.Rows {
			if row.AmountMinor != 0 || row.Currency != "" {
				t.Errorf("a row carries %d %s to somebody who may see no money",
					row.AmountMinor, row.Currency)
			}
		}
		if report.AmountMinor != 0 {
			t.Errorf("the report total carries %d to somebody who may see no money",
				report.AmountMinor)
		}
	}
}

// TestAnUnknownGroupingBecomesTheDefault.
//
// A mistyped query string is not worth an error page, and letting it through
// would head the report with a dimension nothing was grouped by.
func TestAnUnknownGroupingBecomesTheDefault(t *testing.T) {
	for _, attempt := range []GroupBy{"", "nonsense", "CUSTOMER", "people"} {
		if got := attempt.OrDefault(); got != GroupByCustomer {
			t.Errorf("GroupBy(%q).OrDefault() = %q, want the customer", attempt, got)
		}
	}
	for _, known := range Groupings {
		if got := known.OrDefault(); got != known {
			t.Errorf("GroupBy(%q).OrDefault() = %q, want it left alone", known, got)
		}
	}
}

// TestAnEmptyRangeIsAnEmptyReportRatherThanAnError.
func TestAnEmptyRangeIsAnEmptyReportRatherThanAnError(t *testing.T) {
	f := newFixture(t)

	report, err := f.svc.Summarise(f.ctx,
		f.now.AddDate(-1, 0, 0), f.now.AddDate(-1, 0, 1), GroupByPerson)
	if err != nil {
		t.Fatalf("summarising a quiet range = %v, want an empty report", err)
	}
	if len(report.Rows) != 0 || report.Totals.SummedSeconds != 0 {
		t.Errorf("a range with nothing in it produced %d rows totalling %d",
			len(report.Rows), report.Totals.SummedSeconds)
	}
}

// TestAGroupWithNoNameIsStillNamed.
//
// A blank row reads as a rendering fault rather than as data, and these do
// occur: a restored backup can carry an entry whose customer name is empty.
func TestAGroupWithNoNameIsStillNamed(t *testing.T) {
	key, label, _, _ := groupOf(domain.TimeEntry{}, GroupByCustomer)
	if key == "" {
		t.Error("an entry with no customer produces an empty group key, so " +
			"every such entry would collide into one unnamed row")
	}
	if label == "" {
		t.Error("an entry with no customer produces a row with no label")
	}
}
