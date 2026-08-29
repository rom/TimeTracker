package domain

import (
	"testing"
	"time"
)

// The windows a nudge appears in.
//
// Both are about *when* rather than about what, and both are the part that can
// only be wrong in one direction that matters: too early is a panel people learn
// to ignore, which quietly disables the feature that catches a timer left
// running overnight.

// TestPastReminderHourIsLocal.
//
// The hour is the person's, not the server's. An instance serving Stockholm and
// Chicago must nudge each of them at the end of their own day, and the only way
// that is true is if the comparison happens after the instant has been moved
// into their zone.
func TestPastReminderHourIsLocal(t *testing.T) {
	instant := time.Date(2026, 5, 12, 21, 0, 0, 0, time.UTC) // 21:00 UTC

	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}

	// 23:00 in Stockholm, 16:00 in Chicago on the same instant.
	if !PastReminderHour(instant.In(stockholm), 16) {
		t.Error("23:00 in Stockholm is past a 16:00 threshold")
	}
	if !PastReminderHour(instant.In(chicago), 16) {
		t.Error("16:00 in Chicago is past a 16:00 threshold")
	}
	// An hour earlier, Chicago has not reached it and Stockholm has.
	earlier := instant.Add(-2 * time.Hour)
	if PastReminderHour(earlier.In(chicago), 16) {
		t.Error("14:00 in Chicago is not past a 16:00 threshold")
	}
	if !PastReminderHour(earlier.In(stockholm), 16) {
		t.Error("21:00 in Stockholm is past a 16:00 threshold")
	}
}

// TestPastReminderHourClampsAnImpossibleHour.
//
// The hour comes from a settings form. A value outside the day would otherwise
// either nudge always or never, and silently.
func TestPastReminderHourClampsAnImpossibleHour(t *testing.T) {
	noon := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)

	if !PastReminderHour(noon, -5) {
		t.Error("a negative hour should clamp to the start of the day, not disable the nudge")
	}
	if PastReminderHour(noon, 99) {
		t.Error("an hour past the end of the day should clamp to 23, not nudge at noon")
	}
}

// TestWeekIsEnding.
//
// A week is unsubmitted on Wednesday and that is not news. The window opens on
// the last *working* day once the afternoon is going - Friday for a
// Monday-start week, which is when somebody is finishing the week and can still
// act on it - and stays open until the week has gone.
//
// Sunday evening was the first rule, and it is the wrong one: nobody is there to
// read it, so the nudge is cleared unread on Monday morning along with
// everything else.
func TestWeekIsEnding(t *testing.T) {
	monday := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"Wednesday afternoon", time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC), false},
		{"Friday morning", time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC), false},
		{"Friday afternoon", time.Date(2026, 5, 15, 17, 0, 0, 0, time.UTC), true},
		{"Saturday", time.Date(2026, 5, 16, 11, 0, 0, 0, time.UTC), true},
		{"Sunday evening", time.Date(2026, 5, 17, 17, 0, 0, 0, time.UTC), true},
		{"the Monday after", time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC), true},
		{"a fortnight later", time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC), true},
	}
	for _, c := range cases {
		if got := WeekIsEnding(c.now, monday, 16); got != c.want {
			t.Errorf("%s: WeekIsEnding = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWeekIsEndingFollowsTheConfiguredWeekStart.
//
// The week can start on Saturday or Sunday, and the last working day moves with
// it. A rule written as "Friday" would be right by accident for the default and
// wrong for everybody else.
func TestWeekIsEndingFollowsTheConfiguredWeekStart(t *testing.T) {
	// A Sunday-start week: Sunday 10 May to Saturday 16 May. Its last working
	// day is still Friday the 15th.
	sunday := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)

	if WeekIsEnding(time.Date(2026, 5, 14, 17, 0, 0, 0, time.UTC), sunday, 16) {
		t.Error("Thursday of a Sunday-start week is not the end of it")
	}
	if !WeekIsEnding(time.Date(2026, 5, 15, 17, 0, 0, 0, time.UTC), sunday, 16) {
		t.Error("Friday afternoon should open the window in a Sunday-start week too")
	}
	if !WeekIsEnding(time.Date(2026, 5, 16, 11, 0, 0, 0, time.UTC), sunday, 16) {
		t.Error("the Saturday that ends a Sunday-start week is past its last working day")
	}
}

// TestKnownReminderKind.
//
// A dismissal is a write keyed on the kind, so an unrecognised one must not
// reach the table.
func TestKnownReminderKind(t *testing.T) {
	for _, kind := range []ReminderKind{ReminderRunningTimers, ReminderEmptyDay,
		ReminderPendingProposals, ReminderUnsubmittedWeek} {
		if !KnownReminderKind(kind) {
			t.Errorf("%q should be a known reminder", kind)
		}
	}
	for _, kind := range []ReminderKind{"", "nag", "empty_week"} {
		if KnownReminderKind(kind) {
			t.Errorf("%q should not be a known reminder", kind)
		}
	}
}
