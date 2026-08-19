package service

import (
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Copying, routines and switching.
//
// All three exist so that recording ordinary time is cheap. What they must not
// do is create time nobody asked for, so each test checks the refusals as
// carefully as the successes.

// at builds an instant on a day, so a test says 09:00 and means 09:00 rather
// than "nine hours after whatever the fixture's clock happens to read".
func at(day time.Time, hour, minute int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, day.Location())
}

// TestCopyDayReproducesTheDay, including times of day and tags, and prices the
// copy afresh rather than copying the amount.
func TestCopyDayReproducesTheDay(t *testing.T) {
	f := newFixture(t)
	yesterday := f.now.AddDate(0, 0, -1)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(yesterday, 9, 0),
		DurationSeconds: 3 * 3600, Billable: true, Note: "morning",
		Tags: []string{"focus"},
	}); err != nil {
		t.Fatalf("record yesterday: %v", err)
	}
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(yesterday, 13, 0),
		DurationSeconds: 2 * 3600, Billable: true, Note: "afternoon",
		Kind: domain.KindOvertime,
	}); err != nil {
		t.Fatalf("record yesterday: %v", err)
	}

	result, err := f.svc.CopyDay(f.ctx, yesterday, f.now)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if result.Created != 2 {
		t.Fatalf("created %d entries, want 2", result.Created)
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if len(day.Entries) != 2 {
		t.Fatalf("today has %d entries, want 2", len(day.Entries))
	}

	byNote := map[string]domain.TimeEntry{}
	for _, entry := range day.Entries {
		byNote[entry.Note] = entry
	}
	// The time of day comes across, so a copied day looks like the day it came
	// from rather than a pile of entries at nine o'clock.
	if got := byNote["morning"].StartedAt.UTC().Hour(); got != 9 {
		t.Errorf("the morning entry landed at %02d:00, want 09:00", got)
	}
	if got := byNote["afternoon"].StartedAt.UTC().Hour(); got != 13 {
		t.Errorf("the afternoon entry landed at %02d:00, want 13:00", got)
	}
	if len(byNote["morning"].Tags) != 1 {
		t.Errorf("tags did not survive the copy: %v", byNote["morning"].Tags)
	}
	if byNote["afternoon"].KindOrDefault() != domain.KindOvertime {
		t.Error("the kind did not survive the copy")
	}
}

// TestCopyDaySkipsARunningTimer. It has no length yet, so copying it would mean
// either inventing one or starting a second timer nobody asked for.
func TestCopyDaySkipsARunningTimer(t *testing.T) {
	f := newFixture(t)
	yesterday := f.now.AddDate(0, 0, -1)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(yesterday, 9, 0),
		DurationSeconds: 3600, Billable: true, Note: "finished",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A timer started yesterday afternoon and never stopped. The clock moves so
	// that StartTimer, which uses it, records the start on the right day.
	f.now = at(yesterday, 15, 0)
	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "still going"); err != nil {
		t.Fatalf("start: %v", err)
	}
	f.now = at(yesterday.AddDate(0, 0, 1), 9, 0)

	result, err := f.svc.CopyDay(f.ctx, yesterday, f.now)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("created %d, want only the finished entry", result.Created)
	}
	if result.SkippedRunning != 1 {
		t.Errorf("skipped %d running timers, want 1", result.SkippedRunning)
	}
}

// TestCopyIntoALockedWeekIsRefused: copying goes through the same door as
// typing, so the period lock applies to it unchanged.
func TestCopyIntoALockedWeekIsRefused(t *testing.T) {
	f := newFixture(t)
	lastWeek := f.now.AddDate(0, 0, -7)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: lastWeek,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record last week: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, lastWeek); err != nil {
		t.Fatalf("submit: %v", err)
	}

	_, err := f.svc.CopyDay(f.ctx, f.now, lastWeek)
	if err == nil {
		t.Fatal("a copy into a submitted week was allowed")
	}
	if !errors.Is(err, domain.ErrPeriodLocked) {
		t.Errorf("error = %v, want the period lock", err)
	}
}

// TestCopyWeekAlignsDayForDay: Monday's entries land on the target Monday.
func TestCopyWeekAlignsDayForDay(t *testing.T) {
	f := newFixture(t)
	// The fixture's clock is a Monday.
	monday := f.now
	wednesday := monday.AddDate(0, 0, 2)

	for _, when := range []time.Time{at(monday, 9, 0), at(wednesday, 14, 0)} {
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: when,
			DurationSeconds: 3600, Billable: true,
			Note: when.Format("Monday"),
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	nextWeek := monday.AddDate(0, 0, 7)
	result, err := f.svc.CopyWeek(f.ctx, monday, nextWeek)
	if err != nil {
		t.Fatalf("copy week: %v", err)
	}
	if result.Created != 2 {
		t.Fatalf("created %d, want 2", result.Created)
	}

	week, err := f.svc.Week(f.ctx, nextWeek)
	if err != nil {
		t.Fatalf("week view: %v", err)
	}
	if week.Totals.SummedSeconds != 2*3600 {
		t.Errorf("the copied week totals %d seconds, want 7200", week.Totals.SummedSeconds)
	}

	// Each landed on the same weekday it came from.
	day, err := f.svc.Day(f.ctx, nextWeek.AddDate(0, 0, 2))
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if len(day.Entries) != 1 || day.Entries[0].Note != "Wednesday" {
		t.Errorf("the Wednesday entry did not land on Wednesday: %+v", day.Entries)
	}
}

// TestCopyingAnEmptyDayIsRefusedByName rather than quietly doing nothing.
func TestCopyingAnEmptyDayIsRefused(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.CopyDay(f.ctx, f.now.AddDate(0, 0, -1), f.now)
	if err == nil {
		t.Fatal("copying an empty day reported success")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("error = %v, want a validation failure", err)
	}
}

// ---------------------------------------------------------------- routines --

// routine creates a template on the fixture's assignment.
func (f *fixture) routine(t *testing.T, name string, weekdays []int, seconds int64, start string) domain.Routine {
	t.Helper()
	err := f.svc.SaveRoutine(f.ctx, domain.Routine{
		AssignmentID: f.assignment.ID, Name: name, Weekdays: weekdays,
		DurationSeconds: seconds, StartTime: start, Active: true,
	})
	if err != nil {
		t.Fatalf("save routine %q: %v", name, err)
	}
	routines, err := f.svc.Routines(f.ctx, false)
	if err != nil {
		t.Fatalf("list routines: %v", err)
	}
	return routines[len(routines)-1]
}

// TestRoutinesAreOfferedNotFired is the property the whole design rests on:
// having a routine creates nothing until somebody applies it.
func TestRoutinesAreOfferedNotFired(t *testing.T) {
	f := newFixture(t)
	f.routine(t, "Stand-up", []int{1, 2, 3, 4, 5}, 900, "09:15")

	// The fixture's clock is a Monday, so it is due - and the day is still empty.
	due, err := f.svc.RoutinesDue(f.ctx, f.now)
	if err != nil {
		t.Fatalf("routines due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %+v, want the stand-up", due)
	}
	if due[0].AlreadyRecorded {
		t.Error("a routine nobody applied is reported as recorded")
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if len(day.Entries) != 0 {
		t.Fatalf("a routine created %d entries on its own", len(day.Entries))
	}

	// Applying it is what makes the time.
	entry, err := f.svc.ApplyRoutine(f.ctx, due[0].Routine.ID, f.now)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if entry.DurationSeconds != 900 {
		t.Errorf("duration = %d, want the routine's 900", entry.DurationSeconds)
	}
	if got := entry.StartedAt.UTC().Format("15:04"); got != "09:15" {
		t.Errorf("started at %s, want the routine's 09:15", got)
	}

	// And it stops being offered as undone.
	if due, err = f.svc.RoutinesDue(f.ctx, f.now); err != nil {
		t.Fatalf("routines due: %v", err)
	}
	if !due[0].AlreadyRecorded {
		t.Error("an applied routine is still offered as outstanding")
	}
}

// TestRoutinesRespectTheWeekday.
func TestRoutinesRespectTheWeekday(t *testing.T) {
	f := newFixture(t)
	// Fridays only. The fixture's clock is a Monday.
	f.routine(t, "Weekly report", []int{5}, 3600, "16:00")

	if due, err := f.svc.RoutinesDue(f.ctx, f.now); err != nil {
		t.Fatalf("due: %v", err)
	} else if len(due) != 0 {
		t.Errorf("a Friday routine was offered on Monday: %+v", due)
	}

	friday := f.now.AddDate(0, 0, 4)
	if due, err := f.svc.RoutinesDue(f.ctx, friday); err != nil {
		t.Fatalf("due: %v", err)
	} else if len(due) != 1 {
		t.Errorf("a Friday routine was not offered on Friday")
	}

	// Sunday is 7, not 0. Go counts it as 0 and getting that wrong offers
	// Monday's routines on Sunday.
	f.routine(t, "Weekend check", []int{7}, 1800, "")
	sunday := f.now.AddDate(0, 0, 6)
	if due, err := f.svc.RoutinesDue(f.ctx, sunday); err != nil {
		t.Fatalf("due: %v", err)
	} else if len(due) != 1 || due[0].Routine.Name != "Weekend check" {
		t.Errorf("Sunday offered %+v", due)
	}
}

// TestApplyAllRoutinesSkipsWhatIsDone.
func TestApplyAllRoutinesSkipsWhatIsDone(t *testing.T) {
	f := newFixture(t)
	first := f.routine(t, "Stand-up", []int{1, 2, 3, 4, 5}, 900, "09:15")
	f.routine(t, "Lunch", []int{1, 2, 3, 4, 5}, 3600, "12:00")

	if _, err := f.svc.ApplyRoutine(f.ctx, first.ID, f.now); err != nil {
		t.Fatalf("apply one: %v", err)
	}
	created, err := f.svc.ApplyAllRoutines(f.ctx, f.now)
	if err != nil {
		t.Fatalf("apply all: %v", err)
	}
	if created != 1 {
		t.Errorf("apply-all created %d, want only the outstanding one", created)
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day.Entries) != 2 {
		t.Errorf("the day has %d entries, want 2 rather than a duplicated stand-up", len(day.Entries))
	}
}

// TestARoutineBelongsToItsOwner: a routine is a way of typing, not a shared
// object, and applying somebody else's would record their habits as your time.
func TestARoutineBelongsToItsOwner(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	colleagueCtx := auth.WithUser(f.ctx, colleague)

	if err := f.svc.SaveRoutine(f.ctx, domain.Routine{
		AssignmentID: assignment.ID, Name: "Mine", Weekdays: []int{1, 2, 3, 4, 5},
		DurationSeconds: 900, Active: true,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	mine, err := f.svc.Routines(f.ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if theirs, err := f.svc.Routines(colleagueCtx, false); err != nil {
		t.Fatalf("colleague list: %v", err)
	} else if len(theirs) != 0 {
		t.Errorf("the colleague sees %d of somebody else's routines", len(theirs))
	}
	if _, err := f.svc.ApplyRoutine(colleagueCtx, mine[0].ID, f.now); err == nil {
		t.Error("the colleague applied somebody else's routine")
	}
}

// --------------------------------------------------------------- switching --

// TestSwitchStopsEverythingAndStartsOne.
func TestSwitchStopsEverythingAndStartsOne(t *testing.T) {
	f := newFixture(t)
	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	third, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Meetings", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := f.svc.StartTimer(f.ctx, second.ID, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	f.advance(30 * time.Minute)

	result, err := f.svc.SwitchTo(f.ctx, third.ID, "planning")
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	// Both stop. A switch that left one of three going would be worse than
	// either alternative.
	if len(result.Stopped) != 2 {
		t.Errorf("stopped %d timers, want both", len(result.Stopped))
	}

	running, err := f.svc.RunningTimers(f.ctx)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(running) != 1 || running[0].AssignmentID != third.ID {
		t.Fatalf("running = %+v, want only the new one", running)
	}
	// The stopped timers kept their time.
	for _, stopped := range result.Stopped {
		if stopped.DurationSeconds != 1800 {
			t.Errorf("a stopped timer recorded %d seconds, want 1800", stopped.DurationSeconds)
		}
	}
}

// TestSwitchingToWhatIsAlreadyRunningDoesNotSplitIt. Stopping and restarting
// would turn one stretch of work into two entries for no reason.
func TestSwitchingToWhatIsAlreadyRunningDoesNotSplitIt(t *testing.T) {
	f := newFixture(t)
	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	f.advance(20 * time.Minute)

	result, err := f.svc.SwitchTo(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if len(result.Stopped) != 0 {
		t.Errorf("switching to the running timer stopped %d", len(result.Stopped))
	}
	if result.Started.ID != started.ID {
		t.Errorf("a second entry was created: %d then %d", started.ID, result.Started.ID)
	}

	running, err := f.svc.RunningTimers(f.ctx)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(running) != 1 {
		t.Errorf("running = %d, want the original still going", len(running))
	}
}

// TestQuickStartPutsFavouritesFirst, then what is used most.
func TestQuickStartPutsFavouritesFirst(t *testing.T) {
	f := newFixture(t)

	busy, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Busy", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	starred, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Starred", BillableDefault: true,
		Favourite: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The busy one is used far more; the starred one not at all.
	for i := 0; i < 5; i++ {
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: busy.ID, StartedAt: f.now.Add(time.Duration(i) * time.Hour),
			DurationSeconds: 1800, Billable: true,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	quick, err := f.svc.QuickStart(f.ctx, 10)
	if err != nil {
		t.Fatalf("quick start: %v", err)
	}
	if len(quick) < 2 {
		t.Fatalf("quick start returned %d assignments", len(quick))
	}
	if quick[0].ID != starred.ID {
		t.Errorf("first is %q, want the favourite", quick[0].Name)
	}
	if quick[1].ID != busy.ID {
		t.Errorf("second is %q, want the most-used", quick[1].Name)
	}

	// An assignment neither starred nor used does not appear: the list is what
	// somebody works on, not the catalogue.
	for _, item := range quick {
		if item.ID == f.assignment.ID {
			t.Error("an unused, unstarred assignment is in the quick-start list")
		}
	}
}
