package service

import (
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The month overview.
//
// The arithmetic is the same counting rule as everywhere else; what is specific
// to this view is the calendar it is laid out on, and a calendar has three
// things to get wrong: which squares belong to the month, which row a day lands
// in, and what happens at the ends of a month that does not start on a Monday.

// monthFixture records an hour on each of the given days of March 2026 and
// returns the view.
func monthFixture(t *testing.T, f *fixture, days ...int) MonthView {
	t.Helper()

	for _, day := range days {
		at := time.Date(2026, 3, day, 9, 0, 0, 0, time.UTC)
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: at,
			DurationSeconds: 3600, Billable: true,
		}); err != nil {
			t.Fatalf("record time on the %dth: %v", day, err)
		}
	}

	view, err := f.svc.Month(f.ctx, time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	return view
}

// TestTheMonthIsEveryDayOfItAndNothingElse.
//
// March 2026 starts on a Sunday, which makes it the right month to check: with
// weeks starting on Monday the first row holds six days from February, and a
// view that placed them as March days would show work on dates that are not in
// the month being looked at.
func TestTheMonthIsEveryDayOfItAndNothingElse(t *testing.T) {
	f := newFixture(t)
	view := monthFixture(t, f)

	if view.Start.Day() != 1 || view.Start.Month() != time.March {
		t.Errorf("the month starts on %v", view.Start)
	}
	if view.End != view.Start.AddDate(0, 1, 0) {
		t.Errorf("the month ends at %v, want the first of April", view.End)
	}
	if view.DaysInMonth != 31 {
		t.Errorf("March has %d days", view.DaysInMonth)
	}

	inMonth := 0
	seen := map[int]bool{}
	for _, week := range view.Weeks {
		if len(week.Days) != 7 {
			t.Errorf("a calendar row has %d days", len(week.Days))
		}
		for _, day := range week.Days {
			if !day.InMonth {
				continue
			}
			inMonth++
			if seen[day.Date.Day()] {
				t.Errorf("the %dth appears on the calendar twice", day.Date.Day())
			}
			seen[day.Date.Day()] = true
			if day.Date.Month() != time.March {
				t.Errorf("%v is marked as part of March", day.Date)
			}
		}
	}
	if inMonth != 31 {
		t.Errorf("the calendar holds %d days of March, want 31", inMonth)
	}

	// The leading days exist, as blanks: a ragged first row is harder to read
	// than two empty squares.
	if len(view.Weeks) == 0 || view.Weeks[0].Days[0].InMonth {
		t.Error("the first row starts inside the month, so the grid is ragged")
	}
	if len(view.Weekdays) != 7 {
		t.Errorf("the column headings cover %d days", len(view.Weekdays))
	}
}

// TestEachDaysHoursLandOnItsOwnSquare.
func TestEachDaysHoursLandOnItsOwnSquare(t *testing.T) {
	f := newFixture(t)
	view := monthFixture(t, f, 2, 2, 17, 31)

	hours := map[int]int64{}
	for _, week := range view.Weeks {
		for _, day := range week.Days {
			if day.InMonth && day.Seconds > 0 {
				hours[day.Date.Day()] = day.Seconds
			}
		}
	}
	for day, want := range map[int]int64{2: 7200, 17: 3600, 31: 3600} {
		if hours[day] != want {
			t.Errorf("the %dth has %d seconds, want %d", day, hours[day], want)
		}
	}
	if len(hours) != 3 {
		t.Errorf("%d days carry time, want 3: %v", len(hours), hours)
	}
	if view.DaysRecorded != 3 {
		t.Errorf("DaysRecorded = %d, want 3", view.DaysRecorded)
	}
	if view.BusiestDay.Day() != 2 {
		t.Errorf("the busiest day is the %dth, want the 2nd", view.BusiestDay.Day())
	}
	if view.Totals.SummedSeconds != 4*3600 {
		t.Errorf("the month totals %d seconds, want four hours", view.Totals.SummedSeconds)
	}
}

// TestAWeekRowAddsUpToTheSquaresAboveIt.
//
// A row total that included the days from the neighbouring months would be a
// figure nothing on screen adds up to - which on a timesheet is worse than no
// figure at all.
func TestAWeekRowAddsUpToTheSquaresAboveIt(t *testing.T) {
	f := newFixture(t)
	// The 1st is a Sunday, so with Monday weeks it is the last day of the first
	// row; the 2nd starts the second row.
	view := monthFixture(t, f, 1, 2, 3)

	for _, week := range view.Weeks {
		var squares int64
		for _, day := range week.Days {
			if day.InMonth {
				squares += day.Seconds
			}
		}
		if week.Seconds != squares {
			t.Errorf("the week of %v totals %d, but its squares add to %d",
				week.Start.Format("2006-01-02"), week.Seconds, squares)
		}
		if week.Number == 0 {
			t.Errorf("the week of %v has no week number", week.Start)
		}
	}
}

// TestTheMonthSaysWhatItWentOn.
//
// The calendar answers when; this answers what, which is the other half of the
// question somebody opens a month for. Longest first, because the answer is the
// top two lines.
func TestTheMonthSaysWhatItWentOn(t *testing.T) {
	f := newFixture(t)

	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create the second assignment: %v", err)
	}

	// Three hours on Development, one on Support.
	monthFixture(t, f, 3, 4, 5)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: second.ID, StartedAt: time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC),
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record time on Support: %v", err)
	}

	view, err := f.svc.Month(f.ctx, time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("month: %v", err)
	}

	if len(view.ByAssignment) != 2 {
		t.Fatalf("the breakdown has %d rows, want 2: %+v", len(view.ByAssignment), view.ByAssignment)
	}
	if view.ByAssignment[0].Assignment.Name != "Development" {
		t.Errorf("the breakdown is not longest-first: %s is top",
			view.ByAssignment[0].Assignment.Name)
	}
	if view.ByAssignment[0].Percent != 75 {
		t.Errorf("Development is %d%% of the month, want 75", view.ByAssignment[0].Percent)
	}
	if view.ByAssignment[1].Percent != 25 {
		t.Errorf("Support is %d%% of the month, want 25", view.ByAssignment[1].Percent)
	}
}

// TestAnEmptyMonthIsStillACalendar.
//
// The month somebody opens by stepping back too far. It has to be a calendar
// with nothing on it rather than an error or a blank page - and no busiest day,
// which is the field a template would otherwise render as the zero time.
func TestAnEmptyMonthIsStillACalendar(t *testing.T) {
	f := newFixture(t)

	view, err := f.svc.Month(f.ctx, time.Date(2025, 11, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("an empty month: %v", err)
	}
	if len(view.Weeks) == 0 {
		t.Error("an empty month has no calendar")
	}
	if view.DaysRecorded != 0 || view.Totals.SummedSeconds != 0 {
		t.Errorf("an empty month reports %d days and %d seconds",
			view.DaysRecorded, view.Totals.SummedSeconds)
	}
	if !view.BusiestDay.IsZero() {
		t.Errorf("an empty month has a busiest day: %v", view.BusiestDay)
	}
	if len(view.ByAssignment) != 0 {
		t.Error("an empty month went on something")
	}
}

// TestWhatDoesNotCountDoesNotCountHere.
//
// The same rule as every other total (ASR-008): a proposal nobody has accepted
// and an entry flagged for review are questions rather than work. They are still
// *visible* as a mark on the square - a flagged day the owner cannot see is a
// day they will not fix - but they contribute nothing to the figures.
func TestWhatDoesNotCountDoesNotCountHere(t *testing.T) {
	f := newFixture(t)
	view := monthFixture(t, f, 10)

	if view.Totals.SummedSeconds != 3600 {
		t.Fatalf("the fixture is wrong: %d seconds", view.Totals.SummedSeconds)
	}

	// Flag the entry, which is what a review mark does. Through the store,
	// because flagging is something the overlap detector does rather than
	// something a person asks for.
	entry, err := f.db.GetEntry(f.ctx, 1)
	if err != nil {
		t.Fatalf("read the entry: %v", err)
	}
	entry.Flagged = true
	if err := f.db.UpdateEntry(f.ctx, entry); err != nil {
		t.Fatalf("flag the entry: %v", err)
	}
	flagged, err := f.svc.Month(f.ctx, time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	if flagged.Totals.SummedSeconds != 0 {
		t.Errorf("a flagged entry counts towards the month: %d seconds",
			flagged.Totals.SummedSeconds)
	}

	var marked bool
	for _, week := range flagged.Weeks {
		for _, day := range week.Days {
			if day.InMonth && day.Date.Day() == 10 {
				marked = day.Flagged
				if day.Seconds != 0 {
					t.Errorf("the flagged day shows %d seconds", day.Seconds)
				}
				if day.Entries != 1 {
					t.Errorf("the flagged day shows %d entries, want 1", day.Entries)
				}
			}
		}
	}
	if !marked {
		t.Error("the flagged day is not marked, so nobody will go and fix it")
	}
}

// TestSteppingBackFromMarchReachesFebruary.
//
// The link somebody clicks twelve times. Go's AddDate normalises, so a month
// subtracted from the 31st of March is the 3rd of March - which would make the
// previous-month link skip February altogether. Anchoring on the first is what
// avoids it, and this is the test that says so.
func TestSteppingBackFromMarchReachesFebruary(t *testing.T) {
	f := newFixture(t)

	// The 31st: the date a day screen would link from on the last day of March.
	view, err := f.svc.Month(f.ctx, time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	if got := view.PreviousMonth(); got.Month() != time.February || got.Day() != 1 {
		t.Errorf("the month before March is %v, want the 1st of February", got)
	}
	if got := view.NextMonth(); got.Month() != time.April || got.Day() != 1 {
		t.Errorf("the month after March is %v, want the 1st of April", got)
	}

	// And a full year of steps lands on each month exactly once, which is the
	// property the normalising bug breaks.
	months := map[time.Month]int{}
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		view, err := f.svc.Month(f.ctx, at)
		if err != nil {
			t.Fatalf("month %d: %v", i, err)
		}
		months[view.Start.Month()]++
		at = view.NextMonth()
	}
	if len(months) != 12 {
		t.Errorf("stepping forward twelve times visited %d months: %v", len(months), months)
	}
}
