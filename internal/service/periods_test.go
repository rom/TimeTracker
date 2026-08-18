package service

import (
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// The weekly submit and approve workflow, and the lock it puts on a week.
//
// The lock is the part worth testing hardest. A workflow that records a status
// but still lets the hours move underneath it is decoration: the approval would
// be a decision about figures that no longer exist.

// TestSubmitLocksTheWeek: after submitting, the owner cannot add, edit, move or
// delete time inside the week. Each mutation is checked separately, because each
// one is a different call site and the lock is only real if every one of them
// asks.
func TestSubmitLocksTheWeek(t *testing.T) {
	f := newFixture(t)

	existing, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	t.Run("create", func(t *testing.T) {
		_, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: f.now.Add(2 * time.Hour), DurationSeconds: 1800,
		})
		requireLocked(t, err)
	})
	t.Run("update", func(t *testing.T) {
		_, err := f.svc.UpdateEntry(f.ctx, existing.ID, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 7200,
		})
		requireLocked(t, err)
	})
	t.Run("delete", func(t *testing.T) {
		requireLocked(t, f.svc.DeleteEntry(f.ctx, existing.ID))
	})
	t.Run("start a timer", func(t *testing.T) {
		// A timer started inside a submitted week would land as new time in it.
		_, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "after the fact")
		requireLocked(t, err)
	})

	// And the total is unchanged by any of it, which is the point.
	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.TotalSeconds != 3600 {
		t.Errorf("the week moved while locked: %d seconds, want 3600", view.TotalSeconds)
	}
}

// TestAdministratorIsNotExemptFromTheLock: the fixture's user is an
// administrator, and the lock still holds. A lock the most privileged user
// silently walks through is not a lock; reopening is the way through, and it
// leaves a record.
func TestAdministratorIsNotExemptFromTheLock(t *testing.T) {
	f := newFixture(t)
	if f.user.Role != domain.RoleAdmin {
		t.Fatalf("this test needs an administrator, got %s", f.user.Role)
	}

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
	}); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	_, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 60,
	})
	requireLocked(t, err)
}

// TestWithdrawUnlocksTheWeek: the owner can take back an undecided submission
// without needing anybody. Requiring a manager to undo it would make people
// submit late instead, which costs more than it protects.
func TestWithdrawUnlocksTheWeek(t *testing.T) {
	f := newFixture(t)

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 480, // 8 minutes
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := f.svc.WithdrawWeek(f.ctx, f.now); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	// The correction this whole feature is meant to allow: 8 minutes was 8 hours.
	updated, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 8 * 3600,
	})
	if err != nil {
		t.Fatalf("update after withdrawing: %v", err)
	}
	if updated.DurationSeconds != 8*3600 {
		t.Errorf("duration = %d, want %d", updated.DurationSeconds, 8*3600)
	}
}

// TestEditIntoALockedWeekIsRefused: moving an entry out of an open week into a
// submitted one changes the submitted week, so both ends are checked. Without
// this, the lock could be walked around by editing a date.
func TestEditIntoALockedWeekIsRefused(t *testing.T) {
	f := newFixture(t)

	lastWeek := f.now.AddDate(0, 0, -7)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: lastWeek, DurationSeconds: 3600,
	}); err != nil {
		t.Fatalf("create last week's entry: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, lastWeek); err != nil {
		t.Fatalf("submit last week: %v", err)
	}

	// This week is open, so the entry itself can be edited.
	thisWeek, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("create this week's entry: %v", err)
	}

	_, err = f.svc.UpdateEntry(f.ctx, thisWeek.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: lastWeek, DurationSeconds: 3600,
	})
	requireLocked(t, err)
}

// TestEmptyWeekCannotBeSubmitted: an empty submission puts an item in somebody's
// queue with nothing in it to look at.
func TestEmptyWeekCannotBeSubmitted(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err == nil {
		t.Fatal("an empty week was accepted for approval")
	} else if !errors.Is(err, ErrValidation) {
		t.Errorf("error = %v, want a validation failure", err)
	}
}

// TestSingleUserCannotApprove: with one account there is nobody else to approve,
// and pretending otherwise would let a person sign off their own hours through
// the front door.
func TestSingleUserCannotApprove(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
	}); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.CanApprove {
		t.Error("the single-user build offered an approve control")
	}

	weekStart := domain.WeekStartFor(f.now, 1, time.UTC)
	if err := f.svc.ApproveWeek(f.ctx, f.user.ID, weekStart); err == nil {
		t.Error("a user approved their own timesheet")
	}
}

// TestApprovalRequiresSomebodyElse exercises the workflow end to end with two
// real accounts, which is the only configuration in which approval means
// anything.
func TestApprovalRequiresSomebodyElse(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	colleagueCtx := auth.WithUser(f.ctx, colleague)

	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now, DurationSeconds: 7200, Billable: true,
	}); err != nil {
		t.Fatalf("colleague records time: %v", err)
	}
	if _, err := f.svc.SubmitWeek(colleagueCtx, f.now); err != nil {
		t.Fatalf("colleague submits: %v", err)
	}

	weekStart := domain.WeekStartFor(f.now, 1, time.UTC)

	// The submitter cannot approve themselves, whatever else they can do.
	if err := f.svc.ApproveWeek(colleagueCtx, colleague.ID, weekStart); err == nil {
		t.Error("the submitter approved their own week")
	}

	// It is in the administrator's queue, and not in the submitter's.
	queue, err := f.svc.PendingApprovals(f.ctx)
	if err != nil {
		t.Fatalf("pending approvals: %v", err)
	}
	if len(queue) != 1 || queue[0].UserID != colleague.ID {
		t.Fatalf("queue = %+v, want one week belonging to the colleague", queue)
	}
	own, err := f.svc.PendingApprovals(colleagueCtx)
	if err != nil {
		t.Fatalf("submitter's queue: %v", err)
	}
	if len(own) != 0 {
		t.Errorf("the submitter's own week appeared in their approval queue: %+v", own)
	}

	// A rejection needs a reason.
	if err := f.svc.RejectWeek(f.ctx, colleague.ID, weekStart, ""); err == nil {
		t.Error("a week was rejected with no reason")
	}
	if err := f.svc.RejectWeek(f.ctx, colleague.ID, weekStart, "Friday is on the wrong project"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// Rejected means open again, and the reason reaches the owner.
	view, err := f.svc.Period(colleagueCtx, f.now)
	if err != nil {
		t.Fatalf("period after rejection: %v", err)
	}
	if view.Period.Status != domain.PeriodRejected {
		t.Fatalf("status = %s, want rejected", view.Period.Status)
	}
	if view.Period.Locked() {
		t.Error("a rejected week is still locked; the owner cannot correct it")
	}
	if view.Period.Note == "" {
		t.Error("the rejection reason did not reach the owner")
	}

	// Corrected, resubmitted, approved.
	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now.Add(3 * time.Hour), DurationSeconds: 1800,
	}); err != nil {
		t.Fatalf("correct the week: %v", err)
	}
	if _, err := f.svc.SubmitWeek(colleagueCtx, f.now); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	// A resubmission starts clean: the old reason no longer describes it.
	if view, err = f.svc.Period(colleagueCtx, f.now); err != nil {
		t.Fatalf("period after resubmission: %v", err)
	}
	if view.Period.Note != "" {
		t.Errorf("the previous rejection reason survived resubmission: %q", view.Period.Note)
	}
	if err := f.svc.ApproveWeek(f.ctx, colleague.ID, weekStart); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Approved locks it against the owner, and withdrawing is no longer theirs
	// to do.
	day, err := f.svc.Day(colleagueCtx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if len(day.Entries) == 0 {
		t.Fatal("the approved week has no entries to try to delete")
	}
	requireLocked(t, f.svc.DeleteEntry(colleagueCtx, day.Entries[0].ID))
	if err := f.svc.WithdrawWeek(colleagueCtx, f.now); err == nil {
		t.Error("an approved week was withdrawn by its owner")
	}

	// Reopening is the way back, and it is the manager's to do.
	approved, err := f.svc.ApprovedPeriods(f.ctx)
	if err != nil {
		t.Fatalf("approved periods: %v", err)
	}
	if len(approved) != 1 {
		t.Fatalf("approved list = %+v, want the one approved week", approved)
	}
	if err := f.svc.ReopenWeek(f.ctx, colleague.ID, weekStart, "wrong project"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now.Add(5 * time.Hour), DurationSeconds: 600,
	}); err != nil {
		t.Fatalf("the reopened week still refuses changes: %v", err)
	}
}

// TestReopeningIsAudited: unlocking has to leave a record, or an approval means
// nothing after the fact.
func TestReopeningIsAudited(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	colleagueCtx := auth.WithUser(f.ctx, colleague)
	weekStart := domain.WeekStartFor(f.now, 1, time.UTC)

	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
	}); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := f.svc.SubmitWeek(colleagueCtx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := f.svc.ApproveWeek(f.ctx, colleague.ID, weekStart); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := f.svc.ReopenWeek(f.ctx, colleague.ID, weekStart, "an hour was on the wrong project"); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// A submission is recorded against the submitter's own timesheet; a decision
	// about somebody else's is recorded against theirs.
	var actions []string
	for _, resourceID := range []int64{0, colleague.ID} {
		events, err := f.db.ListAuditEvents(f.ctx, "timesheet", resourceID, 200)
		if err != nil {
			t.Fatalf("read the audit trail: %v", err)
		}
		for _, event := range events {
			actions = append(actions, event.Action)
		}
	}
	for _, want := range []string{"timesheet.submit", "timesheet.approved", "timesheet.reopen"} {
		if !containsString(actions, want) {
			t.Errorf("the audit trail has no %q; it holds %v", want, actions)
		}
	}
}

// requireLocked asserts that an error is the period lock refusing a change, and
// not some other failure that happens to be non-nil.
func requireLocked(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a change inside a locked week was allowed")
	}
	if !errors.Is(err, domain.ErrPeriodLocked) {
		t.Fatalf("error = %v, want the period lock", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestApprovalReportShowsWhoHasNotSubmitted.
//
// The queue cannot answer this: a week nobody submitted has no row, so the
// interesting cells are the ones that would otherwise be blank. That is the
// whole reason the report exists, and it is what this test pins.
func TestApprovalReportShowsWhoHasNotSubmitted(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	colleagueCtx := auth.WithUser(f.ctx, colleague)

	lastWeek := f.now.AddDate(0, 0, -7)

	// The colleague works both weeks and submits only the earlier one.
	for _, when := range []time.Time{lastWeek, f.now} {
		if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
			AssignmentID: assignment.ID, StartedAt: when,
			DurationSeconds: 8 * 3600, Billable: true,
		}); err != nil {
			t.Fatalf("record time: %v", err)
		}
	}
	if _, err := f.svc.SubmitWeek(colleagueCtx, lastWeek); err != nil {
		t.Fatalf("submit: %v", err)
	}

	report, err := f.svc.ApprovalReportFor(f.ctx, f.now, 2)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report.Weeks) != 2 {
		t.Fatalf("weeks = %d, want 2", len(report.Weeks))
	}

	row := findRow(t, report, colleague.ID)
	if got := row.Cells[0].Status; got != string(domain.PeriodSubmitted) {
		t.Errorf("the submitted week reads %q", got)
	}
	// The cell that matters: time recorded, nothing submitted.
	if got := row.Cells[1].Status; got != string(StatusNotSubmitted) {
		t.Errorf("the unsubmitted week reads %q, want not_submitted", got)
	}
	if !row.Cells[1].Recorded() {
		t.Error("the unsubmitted week shows no recorded time")
	}
	if row.Outstanding != 1 || report.Outstanding != 1 {
		t.Errorf("outstanding = %d row / %d report, want 1 and 1",
			row.Outstanding, report.Outstanding)
	}
	if report.Weeks[1].NotSubmitted != 1 {
		t.Errorf("the column summary missed it: %+v", report.Weeks[1])
	}
}

// TestApprovalReportLeavesUnworkedWeeksBlank: a week somebody did not work
// needs no submission, and marking it would bury the cells that do.
func TestApprovalReportLeavesUnworkedWeeksBlank(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	colleagueCtx := auth.WithUser(f.ctx, colleague)

	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record time: %v", err)
	}

	report, err := f.svc.ApprovalReportFor(f.ctx, f.now, 4)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	row := findRow(t, report, colleague.ID)

	// Only the last of the four weeks has anything in it.
	for i, cell := range row.Cells[:3] {
		if cell.Status != string(StatusNothing) {
			t.Errorf("week %d is marked %q for a week with no time", i, cell.Status)
		}
	}
	if row.Outstanding != 1 {
		t.Errorf("outstanding = %d, want only the worked week", row.Outstanding)
	}
}

// TestApprovalReportIsScopedToWhatTheActorMaySee: without the manage permission
// the report is the actor's own weeks. Useful, and gives away nothing about
// anybody else.
func TestApprovalReportIsScopedToWhatTheActorMaySee(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	colleagueCtx := auth.WithUser(f.ctx, colleague)

	// Both people record time.
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("admin records time: %v", err)
	}
	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("colleague records time: %v", err)
	}

	// The administrator sees both.
	managerView, err := f.svc.ApprovalReportFor(f.ctx, f.now, 1)
	if err != nil {
		t.Fatalf("manager report: %v", err)
	}
	if len(managerView.Rows) != 2 {
		t.Errorf("a manager sees %d rows, want both people", len(managerView.Rows))
	}

	// The member sees only themselves.
	memberView, err := f.svc.ApprovalReportFor(colleagueCtx, f.now, 1)
	if err != nil {
		t.Fatalf("member report: %v", err)
	}
	if len(memberView.Rows) != 1 {
		t.Fatalf("a member sees %d rows, want only their own", len(memberView.Rows))
	}
	if memberView.Rows[0].UserID != colleague.ID {
		t.Errorf("a member's report shows somebody else: %+v", memberView.Rows[0])
	}
}

// TestApprovalReportClampsItsRange: an unbounded number from a query string
// must not become an unbounded query.
func TestApprovalReportClampsItsRange(t *testing.T) {
	f := newServerFixture(t)

	for _, weeks := range []int{0, -5, 10000} {
		report, err := f.svc.ApprovalReportFor(f.ctx, f.now, weeks)
		if err != nil {
			t.Fatalf("weeks=%d: %v", weeks, err)
		}
		if len(report.Weeks) < 1 || len(report.Weeks) > 53 {
			t.Errorf("weeks=%d produced %d columns", weeks, len(report.Weeks))
		}
	}
}

// findRow locates one person's row, failing the test if it is absent.
func findRow(t *testing.T, report ApprovalReport, userID int64) ApprovalRow {
	t.Helper()
	for _, row := range report.Rows {
		if row.UserID == userID {
			return row
		}
	}
	t.Fatalf("no row for user %d in %+v", userID, report.Rows)
	return ApprovalRow{}
}
