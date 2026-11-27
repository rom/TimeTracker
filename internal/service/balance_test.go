package service

import (
	"context"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// The running over or under.
//
// The fixture's clock sits on Monday 2026-03-16, so the three whole weeks
// before it begin on 23 February, 2 March and 9 March. A forty-hour week is
// 144 000 seconds; three of them are 432 000.

const fortyHours = 40 * 3600

// countBalanceFrom turns the balance on at a date, through the real settings
// path.
func (f *fixture) countBalanceFrom(t *testing.T, date string) {
	t.Helper()

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
		ExpectedWeeklyMinutes: 40 * 60,
		BalanceStartsOn:       date,
	}); err != nil {
		t.Fatalf("set the balance start: %v", err)
	}
}

// reloaded returns a context carrying this person's account as it now stands,
// which is what every request has and a long-lived test context does not.
func (f *fixture) reloaded(t *testing.T) context.Context {
	t.Helper()

	user, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return auth.WithUser(f.ctx, user)
}

// record puts time on a day, through the service.
func (f *fixture) record(t *testing.T, day string, seconds int64) {
	t.Helper()

	start, err := time.Parse("2006-01-02T15:04:05Z", day+"T09:00:00Z")
	if err != nil {
		t.Fatalf("parse %s: %v", day, err)
	}
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: start,
		DurationSeconds: seconds, Note: "Work",
		// As the add form does: the assignment decides, and the fixture's is
		// billable, so this is chargeable time with an amount on it.
		BillableFromAssignment: true,
	}); err != nil {
		t.Fatalf("record %s: %v", day, err)
	}
}

func TestNoBalanceUntilTheInstanceAsksForOne(t *testing.T) {
	f := newFixture(t)
	f.record(t, "2026-03-09", 8*3600)

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.Balance.Counted() {
		t.Error("a balance was shown without anybody turning it on; a running " +
			"figure is read as hours owed by people who did not ask to be measured")
	}
}

func TestTheBalanceCountsWholeWeeksOnly(t *testing.T) {
	f := newFixture(t)
	f.countBalanceFrom(t, "2026-03-09")

	// A full week in the week before this one, and a single day in the week
	// on screen. Only the first may count: a balance that included the week
	// in progress would read as thirty-two hours down every Monday morning.
	for _, day := range []string{"2026-03-09", "2026-03-10", "2026-03-11", "2026-03-12", "2026-03-13"} {
		f.record(t, day, 8*3600)
	}
	f.record(t, "2026-03-16", 2*3600)

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if !view.Balance.Counted() {
		t.Fatal("no balance, with a whole week behind this one")
	}
	if view.Balance.Weeks != 1 {
		t.Errorf("%d weeks counted, want 1", view.Balance.Weeks)
	}
	if view.Balance.Seconds != 0 {
		t.Errorf("balance is %d seconds, want 0: forty hours worked in a "+
			"forty-hour week, and this week's two hours must not count",
			view.Balance.Seconds)
	}
	// This week's own figure is untouched by any of that.
	if view.TotalSeconds != 2*3600 {
		t.Errorf("this week totals %d seconds, want 7200", view.TotalSeconds)
	}
}

func TestWorkingLessThanAFullWeekPutsTheBalanceBehind(t *testing.T) {
	f := newFixture(t)
	f.countBalanceFrom(t, "2026-03-02")

	// Two whole weeks owed, one day worked.
	f.record(t, "2026-03-03", 8*3600)

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	want := int64(8*3600 - 2*fortyHours)
	if view.Balance.Seconds != want {
		t.Errorf("balance is %d, want %d (one day against two forty-hour weeks)",
			view.Balance.Seconds, want)
	}
	if view.Balance.Weeks != 2 {
		t.Errorf("%d weeks counted, want 2", view.Balance.Weeks)
	}
}

// A fortnight off is two complete weeks, not eighty hours owed. This is the
// same rule the week view applies to one week, and the one most likely to be
// got wrong over a range.
func TestAWeekOffLeavesTheBalanceWhereItWas(t *testing.T) {
	f := newFixture(t)
	f.countBalanceFrom(t, "2026-03-09")

	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind: domain.AbsenceHoliday, StartsOn: "2026-03-09", EndsOn: "2026-03-13",
	}); err != nil {
		t.Fatalf("record absence: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.Balance.Seconds != 0 {
		t.Errorf("balance is %d after a week's holiday, want 0: a week off is "+
			"a complete week, not forty hours owed", view.Balance.Seconds)
	}
	if view.Balance.AwaySeconds != fortyHours {
		t.Errorf("away accounts for %d seconds, want %d",
			view.Balance.AwaySeconds, int64(fortyHours))
	}
}

// Somebody who joined after the count began does not inherit the months before
// they arrived - and cannot award themselves a longer run at it either.
func TestAPersonalStartDateOnlyEverShortensTheCount(t *testing.T) {
	f := newFixture(t)
	f.countBalanceFrom(t, "2026-02-23")

	// Three whole weeks behind this one, so three weeks owed and nothing done.
	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.Balance.Weeks != 3 {
		t.Fatalf("%d weeks counted, want 3", view.Balance.Weeks)
	}

	// Joined on 9 March: one week owed, not three.
	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		ClockFormat: "24h", DayStartHour: -1, DayEndHour: -1,
		ExpectedWeeklyMinutes: -1, BalanceStartsOn: "2026-03-09",
	}); err != nil {
		t.Fatalf("set a personal start: %v", err)
	}
	// Read back through a context carrying the reloaded account, as a request
	// does: the balance reads the owner out of the context, so a stale copy
	// would silently keep the instance's date.
	if view, err = f.svc.Period(f.reloaded(t), f.now); err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.Balance.Weeks != 1 {
		t.Errorf("%d weeks counted after joining on 9 March, want 1", view.Balance.Weeks)
	}

	// An earlier personal date is ignored: the instance decides how far back
	// anybody is measured.
	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		ClockFormat: "24h", DayStartHour: -1, DayEndHour: -1,
		ExpectedWeeklyMinutes: -1, BalanceStartsOn: "2020-01-01",
	}); err != nil {
		t.Fatalf("set an earlier personal start: %v", err)
	}
	if view, err = f.svc.Period(f.reloaded(t), f.now); err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.Balance.Weeks != 3 {
		t.Errorf("%d weeks counted with a personal date before the instance's, "+
			"want 3: an account cannot award itself a longer run at the figure",
			view.Balance.Weeks)
	}
}

func TestTheMonthIsMeasuredAgainstItsWorkingDays(t *testing.T) {
	f := newFixture(t)

	// March 2026 has 22 working days: 176 hours at eight a day.
	month, err := f.svc.Month(f.ctx, f.now)
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	if month.ExpectedSeconds != 22*8*3600 {
		t.Errorf("March expects %d seconds, want %d (22 working days of eight hours)",
			month.ExpectedSeconds, 22*8*3600)
	}

	// A day's holiday fills its own share of that, and no more.
	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind: domain.AbsenceHoliday, StartsOn: "2026-03-10", EndsOn: "2026-03-10",
	}); err != nil {
		t.Fatalf("record absence: %v", err)
	}
	if month, err = f.svc.Month(f.ctx, f.now); err != nil {
		t.Fatalf("month: %v", err)
	}
	if month.AbsentDays != 1 || month.AbsentSeconds != 8*3600 {
		t.Errorf("one day away accounts for %d days and %d seconds, want 1 and %d",
			month.AbsentDays, month.AbsentSeconds, 8*3600)
	}
}
