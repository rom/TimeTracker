package domain

import (
	"testing"
	"time"
)

// WeekStartFor decides which week an entry belongs to, and therefore which lock
// applies to it. An off-by-one here would lock the wrong week - silently, since
// both answers look like a plausible date.

func TestWeekStartFor(t *testing.T) {
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}

	cases := []struct {
		name      string
		when      time.Time
		weekStart int
		loc       *time.Location
		want      string
	}{
		{
			name: "Monday is its own week start",
			when: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), weekStart: 1, loc: time.UTC,
			want: "2026-03-16",
		},
		{
			name: "Sunday belongs to the week that began the previous Monday",
			when: time.Date(2026, 3, 22, 23, 0, 0, 0, time.UTC), weekStart: 1, loc: time.UTC,
			want: "2026-03-16",
		},
		{
			// Go counts Sunday as 0 and ISO counts it as 7. Getting that wrong
			// puts Sunday six days into the future.
			name: "Sunday-start weeks put Sunday first",
			when: time.Date(2026, 3, 22, 12, 0, 0, 0, time.UTC), weekStart: 7, loc: time.UTC,
			want: "2026-03-22",
		},
		{
			// Late Sunday evening in UTC is already Monday in Stockholm, so the
			// entry belongs to the following week for someone working there.
			name: "the owner's zone decides, not UTC",
			when: time.Date(2026, 3, 22, 23, 30, 0, 0, time.UTC), weekStart: 1, loc: stockholm,
			want: "2026-03-23",
		},
		{
			name: "an out-of-range setting falls back to Monday",
			when: time.Date(2026, 3, 18, 9, 0, 0, 0, time.UTC), weekStart: 0, loc: time.UTC,
			want: "2026-03-16",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WeekStartFor(tc.when, tc.weekStart, tc.loc); got != tc.want {
				t.Errorf("WeekStartFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPeriodLocking: which statuses refuse changes. Rejected is deliberately
// open - the owner has been asked to correct it, so refusing the correction
// would be absurd.
func TestPeriodLocking(t *testing.T) {
	for _, tc := range []struct {
		status PeriodStatus
		locked bool
	}{
		{PeriodOpen, false},
		{PeriodSubmitted, true},
		{PeriodApproved, true},
		{PeriodRejected, false},
	} {
		period := TimesheetPeriod{Status: tc.status}
		if period.Locked() != tc.locked {
			t.Errorf("%s: Locked = %v, want %v", tc.status, period.Locked(), tc.locked)
		}
		if period.Editable() == tc.locked {
			t.Errorf("%s: Editable disagrees with Locked", tc.status)
		}
	}
}

// TestPeriodChanged flags a submitted week whose total has moved. It should not
// be possible - the week is locked - but a restored backup can do it, and a
// manager approving figures that are no longer the submitted ones is exactly
// what this exists to prevent.
func TestPeriodChanged(t *testing.T) {
	steady := TimesheetPeriod{Status: PeriodSubmitted, SubmittedSeconds: 3600, CurrentSeconds: 3600}
	if steady.Changed() {
		t.Error("an unchanged week was flagged as changed")
	}

	moved := TimesheetPeriod{Status: PeriodSubmitted, SubmittedSeconds: 3600, CurrentSeconds: 7200}
	if !moved.Changed() {
		t.Error("a week that moved after submission was not flagged")
	}

	// Only submitted weeks are compared: an open week has no submitted figure to
	// differ from, and flagging every one of them would make the flag meaningless.
	open := TimesheetPeriod{Status: PeriodOpen, SubmittedSeconds: 0, CurrentSeconds: 7200}
	if open.Changed() {
		t.Error("an open week was flagged as changed")
	}
}

func TestParseWeekStart(t *testing.T) {
	if _, err := ParseWeekStart("2026-03-16", time.UTC); err != nil {
		t.Errorf("a valid week start was rejected: %v", err)
	}
	if _, err := ParseWeekStart("week 12", time.UTC); err == nil {
		t.Error("a nonsense week start was accepted")
	}

	start, err := ParseWeekStart("2026-03-16", time.UTC)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if end := WeekEnd(start); !end.Equal(time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("WeekEnd = %s, want the following Monday", end)
	}
}
