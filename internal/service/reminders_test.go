package service

import (
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// End-of-day and end-of-week nudges.
//
// A reminder is computed from the timesheet rather than stored, so the tests are
// about when it is true: too early and people learn to ignore the panel, which
// disables the one nudge that catches a timer left running overnight.

// kinds lists what a set of reminders is about, for comparing against an
// expectation without depending on their order.
func kinds(reminders []Reminder) map[domain.ReminderKind]int {
	out := map[domain.ReminderKind]int{}
	for _, r := range reminders {
		out[r.Kind] = r.Count
	}
	return out
}

// TestRemindersWaitForTheEndOfTheDay.
//
// The nudges are about finishing a day, so they have to arrive while it can
// still be finished - and not at eleven in the morning, when everything they
// say is both true and useless.
func TestRemindersWaitForTheEndOfTheDay(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 11, 0)

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, ""); err != nil {
		t.Fatalf("start timer: %v", err)
	}

	morning, err := f.svc.Reminders(f.ctx)
	if err != nil {
		t.Fatalf("reminders: %v", err)
	}
	if len(morning) != 0 {
		t.Errorf("%d reminders at 11:00, want none before the reminder hour", len(morning))
	}

	f.now = at(f.now, 17, 0)
	evening, err := f.svc.Reminders(f.ctx)
	if err != nil {
		t.Fatalf("reminders: %v", err)
	}
	got := kinds(evening)
	if got[domain.ReminderRunningTimers] != 1 {
		t.Errorf("reminders at 17:00 = %v, want one running timer", got)
	}
	// The timer is running, so the day is not empty and that nudge must not
	// appear alongside it.
	if _, empty := got[domain.ReminderEmptyDay]; empty {
		t.Error("a day with a running timer is not an empty day")
	}
}

// TestReminderAboutAnEmptyDay.
//
// The one that catches somebody who never opened the application: nothing
// recorded, no timer, and the afternoon gone.
func TestReminderAboutAnEmptyDay(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 17, 0)

	got := kinds(mustReminders(t, f))
	if _, ok := got[domain.ReminderEmptyDay]; !ok {
		t.Errorf("reminders = %v, want one about the empty day", got)
	}

	// Recording something makes the nudge untrue, and an untrue nudge is not
	// computed - there is nothing stored to go and clear.
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record entry: %v", err)
	}
	if _, ok := kinds(mustReminders(t, f))[domain.ReminderEmptyDay]; ok {
		t.Error("the empty-day nudge survived the day stopping being empty")
	}
}

// TestDismissingAReminderIsForThatDayOnly.
//
// A nudge you cannot wave away is nagging, and one waved away for good is a
// feature quietly switched off. The scope is the day, so tomorrow asks again.
func TestDismissingAReminderIsForThatDayOnly(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 17, 0)
	today := domain.DayScope(f.now)

	if _, ok := kinds(mustReminders(t, f))[domain.ReminderEmptyDay]; !ok {
		t.Fatal("expected an empty-day nudge to dismiss")
	}
	if err := f.svc.DismissReminder(f.ctx, domain.ReminderEmptyDay, today); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if _, ok := kinds(mustReminders(t, f))[domain.ReminderEmptyDay]; ok {
		t.Error("the nudge came back after being dismissed")
	}

	// Dismissing twice is one row and not an error: two tabs, or a double click.
	if err := f.svc.DismissReminder(f.ctx, domain.ReminderEmptyDay, today); err != nil {
		t.Errorf("dismissing twice: %v", err)
	}

	// Tomorrow is a different day and a different question.
	f.now = f.now.AddDate(0, 0, 1)
	if _, ok := kinds(mustReminders(t, f))[domain.ReminderEmptyDay]; !ok {
		t.Error("yesterday's dismissal silenced today's nudge")
	}
}

// TestReminderAboutAnUnsubmittedWeek.
//
// The week nudge waits for the week to be ending, ignores an empty week
// entirely, and goes away when the week is submitted.
func TestReminderAboutAnUnsubmittedWeek(t *testing.T) {
	f := newFixture(t)
	// The fixture's Monday. Nothing recorded yet, so an empty week.
	monday := f.now
	f.now = at(monday.AddDate(0, 0, 6), 17, 0) // Sunday evening

	if _, ok := kinds(mustReminders(t, f))[domain.ReminderUnsubmittedWeek]; ok {
		t.Error("an empty week was nudged about; a week with no time is somebody's holiday")
	}

	f.now = at(monday, 9, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(monday, 9, 0),
		DurationSeconds: 4 * 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record entry: %v", err)
	}

	// Wednesday afternoon: unsubmitted, and not news.
	f.now = at(monday.AddDate(0, 0, 2), 17, 0)
	if _, ok := kinds(mustReminders(t, f))[domain.ReminderUnsubmittedWeek]; ok {
		t.Error("a week was nudged about on Wednesday, which is when every week is unsubmitted")
	}

	// Sunday evening: worth saying.
	f.now = at(monday.AddDate(0, 0, 6), 17, 0)
	reminders := mustReminders(t, f)
	if _, ok := kinds(reminders)[domain.ReminderUnsubmittedWeek]; !ok {
		t.Fatalf("reminders on Sunday evening = %v, want one about the week", kinds(reminders))
	}
	// It names the weekdays with nothing on them, as an aside rather than as
	// four more nudges.
	for _, r := range reminders {
		if r.Kind != domain.ReminderUnsubmittedWeek {
			continue
		}
		if len(r.Days) != 4 {
			t.Errorf("the week nudge names %d empty weekdays, want Tuesday to Friday", len(r.Days))
		}
		for _, day := range r.Days {
			if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
				t.Errorf("%s is a weekend and is not a hole in a timesheet", day.Weekday())
			}
		}
	}

	if _, err := f.svc.SubmitWeek(f.ctx, monday); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, ok := kinds(mustReminders(t, f))[domain.ReminderUnsubmittedWeek]; ok {
		t.Error("the week nudge survived the week being submitted")
	}
}

// TestRemindersCanBeSwitchedOff.
//
// An instance that does not want to nudge anybody says so once, and no screen
// computes any of this afterwards.
func TestRemindersCanBeSwitchedOff(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 17, 0)

	if len(mustReminders(t, f)) == 0 {
		t.Fatal("expected at least one reminder before switching them off")
	}
	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", DefaultRate: "0",
		WeekStart: 1, RemindersDisabled: true,
	}); err != nil {
		t.Fatalf("switch reminders off: %v", err)
	}
	if got := mustReminders(t, f); len(got) != 0 {
		t.Errorf("%d reminders with the feature off, want none", len(got))
	}
}

// TestDismissingRefusesWhatItCannotStore.
//
// A dismissal is a row read back forever, keyed on a kind and a date. Anything
// else would be a row nothing ever matches again.
func TestDismissingRefusesWhatItCannotStore(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.DismissReminder(f.ctx, "stop-nagging", domain.DayScope(f.now)); err == nil {
		t.Error("an unknown reminder kind should be refused")
	}
	if err := f.svc.DismissReminder(f.ctx, domain.ReminderEmptyDay, "whenever"); err == nil {
		t.Error("a scope that is not a date should be refused")
	}
}

func mustReminders(t *testing.T, f *fixture) []Reminder {
	t.Helper()
	reminders, err := f.svc.Reminders(f.ctx)
	if err != nil {
		t.Fatalf("reminders: %v", err)
	}
	return reminders
}
