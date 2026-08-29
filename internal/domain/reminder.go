package domain

import "time"

// Reminders: end-of-day and end-of-week nudges.
//
// A reminder is a statement about the timesheet as it is, not a message that was
// sent. It appears when it is true, and stops appearing when it stops being true
// - so the only state worth storing is a dismissal
// (ADR-0034).

// ReminderKind identifies a nudge. The value is stored in a dismissal row, so
// renaming one silently un-dismisses it for everybody; they are written out
// rather than derived for that reason.
type ReminderKind string

const (
	// ReminderRunningTimers: a timer is still going at the end of the day. The
	// most valuable of the four, because it is the mistake that costs hours
	// rather than minutes.
	ReminderRunningTimers ReminderKind = "running_timers"
	// ReminderEmptyDay: nothing recorded today.
	ReminderEmptyDay ReminderKind = "empty_day"
	// ReminderPendingProposals: somebody recorded time for you and it counts for
	// nothing until you decide about it.
	ReminderPendingProposals ReminderKind = "pending_proposals"
	// ReminderUnsubmittedWeek: the week is over, has time in it, and nobody has
	// submitted it.
	ReminderUnsubmittedWeek ReminderKind = "unsubmitted_week"
)

// KnownReminderKind reports whether k is a nudge this application produces.
//
// Dismissal is a write keyed on this value, so an unrecognised one must not
// reach the table: rows nothing will ever read again are how a small table stops
// being small.
func KnownReminderKind(k ReminderKind) bool {
	switch k {
	case ReminderRunningTimers, ReminderEmptyDay, ReminderPendingProposals,
		ReminderUnsubmittedWeek:
		return true
	}
	return false
}

// Reminder is one nudge, with what it needs to say and where to act on it.
type Reminder struct {
	Kind ReminderKind
	// Scope is the day or the week start this is about, as YYYY-MM-DD, and is
	// what a dismissal is recorded against - so waving away today's nudge says
	// nothing about tomorrow's.
	Scope string
	// Count is how many of whatever it is: timers still running, proposals
	// waiting. Zero where the nudge is not about a number.
	Count int
	// Days names the weekdays of an unsubmitted week that have no time on them,
	// as an aside rather than as separate nudges - four reminders about one week
	// is nagging, one reminder that mentions four days is information.
	Days []time.Time
	// Weekly separates the end-of-week nudge from the end-of-day ones, since the
	// two appear on different screens and under different conditions.
	Weekly bool
}

// DayScope formats a day as a dismissal scope.
func DayScope(day time.Time) string { return day.Format("2006-01-02") }

// PastReminderHour reports whether the local day has reached the hour at which
// end-of-day nudges start.
//
// The hour is local to the person, not to the server: a reminder about finishing
// the day has to arrive at the end of *their* day, and an instance serving two
// time zones would otherwise nudge one of them at breakfast.
func PastReminderHour(now time.Time, hour int) bool {
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = 23
	}
	return now.Hour() >= hour
}

// WeekIsEnding reports whether a week has reached the point where an
// unsubmitted-week nudge is worth showing.
//
// From the last *working* day of the week once the reminder hour has passed, and
// at any point after the week is over. The last working day rather than the last
// day, because for a Monday-start week that is Friday afternoon - which is when
// somebody is finishing the week and can still do something about it. Sunday
// evening is when they are not at work, and a nudge nobody is there to read is
// one that gets cleared unread on Monday.
//
// Not earlier: a week is normally unsubmitted on Wednesday, and saying so on
// Wednesday teaches people to ignore the panel.
func WeekIsEnding(now, weekStart time.Time, hour int) bool {
	if now.After(weekStart.AddDate(0, 0, 7)) {
		return true
	}
	last := lastWorkingDay(weekStart)
	if now.Before(last) {
		return false
	}
	if SameDay(now, last) {
		return PastReminderHour(now, hour)
	}
	return true
}

// lastWorkingDay is the final Monday-to-Friday day inside a week.
//
// Falls back to the last day of the week if there is somehow no weekday in it,
// which cannot happen for any seven-day window but keeps this total rather than
// relying on that.
func lastWorkingDay(weekStart time.Time) time.Time {
	for offset := 6; offset >= 0; offset-- {
		day := weekStart.AddDate(0, 0, offset)
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			return day
		}
	}
	return weekStart.AddDate(0, 0, 6)
}

// SameDay reports whether two instants fall on the same calendar day, in
// whatever zone they carry.
func SameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
