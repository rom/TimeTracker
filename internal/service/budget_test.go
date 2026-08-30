package service

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Budget consumption and burn.
//
// The arithmetic is proved in the domain. These are about what reaches the
// report from the database: which entries count as consumption, which projects
// appear at all, and who may look.

// budgetProject gives the fixture's project a budget in hours.
func budgetProject(t *testing.T, f *fixture, hours int64, minor int64) {
	t.Helper()
	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	project.BudgetSeconds = hours * 3600
	project.BudgetMinor = minor
	if err := f.svc.UpdateProject(f.ctx, project); err != nil {
		t.Fatalf("set budget: %v", err)
	}
}

// record adds a stopped entry some days before the fixture's clock.
func record(t *testing.T, f *fixture, daysAgo int, hours int64) domain.TimeEntry {
	t.Helper()
	start := at(f.now.AddDate(0, 0, -daysAgo), 9, 0)
	end := start.Add(time.Duration(hours) * time.Hour)
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: start, EndedAt: &end,
		Billable: true,
	})
	if err != nil {
		t.Fatalf("record entry: %v", err)
	}
	return entry
}

// firstRow is the report's only row, since the fixture has one project.
func firstRow(t *testing.T, f *fixture) BudgetRow {
	t.Helper()
	report, err := f.svc.BudgetReportFor(f.ctx, f.now)
	if err != nil {
		t.Fatalf("budget report: %v", err)
	}
	if len(report.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(report.Rows))
	}
	return report.Rows[0]
}

// TestBudgetReportCountsWhatItShould.
//
// Consumption is the same "counting" rule every total uses: confirmed and not
// flagged. An entry marked for review is a question rather than consumption, and
// a report that counted it would show a project over its cap because somebody
// mistyped an hour.
func TestBudgetReportCountsWhatItShould(t *testing.T) {
	f := newFixture(t)
	budgetProject(t, f, 100, 0)

	record(t, f, 7, 6)
	flagged := record(t, f, 6, 40)

	// Flag the big one by hand, the way a long-running timer would be.
	entry, err := f.db.GetEntry(f.ctx, flagged.ID)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	entry.Flagged = true
	if err := f.db.UpdateEntry(f.ctx, entry); err != nil {
		t.Fatalf("flag entry: %v", err)
	}

	row := firstRow(t, f)
	if row.UsedSeconds != 6*3600 {
		t.Errorf("used %ds, want 6h - the flagged forty hours are a question, not consumption",
			row.UsedSeconds)
	}
	if row.UsedPercent() != 6 {
		t.Errorf("used %d%%, want 6", row.UsedPercent())
	}
}

// TestBudgetReportOnlyListsBudgetedProjects.
//
// A project with no budget has nothing to report against, and listing every
// project with an empty row is how a report becomes something nobody opens.
func TestBudgetReportOnlyListsBudgetedProjects(t *testing.T) {
	f := newFixture(t)
	record(t, f, 3, 8)

	report, err := f.svc.BudgetReportFor(f.ctx, f.now)
	if err != nil {
		t.Fatalf("budget report: %v", err)
	}
	if len(report.Rows) != 0 {
		t.Errorf("%d rows for a project with no budget, want none", len(report.Rows))
	}

	budgetProject(t, f, 40, 0)
	if len(firstRow(t, f).ProjectName) == 0 {
		t.Error("the project should appear once it has a budget")
	}
}

// TestBudgetReportBurnUsesTheRecentWindow.
//
// Old work counts towards consumption and not towards the rate. A project that
// ran hard a year ago and is quiet now must not be projected to run out next
// week.
func TestBudgetReportBurnUsesTheRecentWindow(t *testing.T) {
	f := newFixture(t)
	budgetProject(t, f, 200, 0)

	// Long ago: counts as used, and says nothing about the current rate.
	record(t, f, 200, 8)
	record(t, f, 199, 8)

	row := firstRow(t, f)
	if row.UsedSeconds != 16*3600 {
		t.Errorf("used %ds, want 16h from the old work", row.UsedSeconds)
	}
	if row.Burn.Projected() {
		t.Errorf("projected a date from work done half a year ago (reason %q)", row.Burn.Reason)
	}
	if row.Burn.Reason != "no_recent_activity" {
		t.Errorf("reason = %q, want no_recent_activity", row.Burn.Reason)
	}

	// Two recent weeks of work, and the rate becomes knowable.
	record(t, f, 3, 10)
	record(t, f, 10, 10)

	row = firstRow(t, f)
	if !row.Burn.Projected() {
		t.Fatalf("still no projection after two active weeks: %q", row.Burn.Reason)
	}
	if row.Burn.WeeklySeconds != 10*3600 {
		t.Errorf("rate = %ds/week, want 10h", row.Burn.WeeklySeconds)
	}
}

// TestBudgetReportIsScopedAndPermissioned.
//
// A budget is a commercial fact about an engagement. A member sees no report at
// all rather than an empty one - an empty table with a heading tells somebody
// there is something they are not being shown, which is its own leak.
func TestBudgetReportIsScopedAndPermissioned(t *testing.T) {
	f := newFixture(t)
	budgetProject(t, f, 100, 0)
	record(t, f, 3, 8)

	// The fixture's authoriser permits everything, so this asserts the service
	// asks at all - swap in the real one and a member is refused.
	rbac := newRBACFixture(t, f, domain.RoleMember)
	if _, err := rbac.svc.BudgetReportFor(rbac.ctx, f.now); !errors.Is(err, ErrNotFound) {
		t.Errorf("a member's budget report = %v, want not found", err)
	}
}

// newRBACFixture rebuilds the fixture's service behind the real authoriser,
// acting as a user in the given role.
func newRBACFixture(t *testing.T, f *fixture, role domain.Role) *fixture {
	t.Helper()

	user, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Someone", Role: role, TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(f.db, auth.RoleAuthorizer{IsProjectMember: f.db.IsProjectMember}, logger,
		func() time.Time { return f.now })
	return &fixture{db: f.db, svc: svc, now: f.now, ctx: auth.WithUser(f.ctx, user)}
}
