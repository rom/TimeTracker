package domain

import (
	"fmt"
	"time"
)

// A timesheet period is one person's week, and the unit at which hours are
// declared finished, checked, and frozen.
//
// The period is the unit of locking rather than the individual entry. Locking
// entries one at a time would leave a week half-frozen, and the question people
// actually ask - "can I still fix last week?" - would have no simple answer.

// PeriodStatus is where a week has got to.
type PeriodStatus string

const (
	// PeriodOpen is the default. A week with no stored row is open, which is why
	// the feature costs nothing for anyone who never uses it.
	PeriodOpen PeriodStatus = "open"
	// PeriodSubmitted means the owner has declared it finished. It is locked to
	// them, but a manager can still reject it back to open.
	PeriodSubmitted PeriodStatus = "submitted"
	// PeriodApproved means a manager has accepted it. Locked to everyone until
	// deliberately reopened.
	PeriodApproved PeriodStatus = "approved"
	// PeriodRejected means a manager sent it back with a reason. Open again for
	// the owner to correct.
	PeriodRejected PeriodStatus = "rejected"
)

// TimesheetPeriod is one person's week.
type TimesheetPeriod struct {
	ID     int64
	UserID int64
	// WeekStart is the first day of the week, as YYYY-MM-DD in the owner's zone.
	WeekStart   string
	Status      PeriodStatus
	SubmittedAt time.Time
	DecidedBy   int64
	DecidedAt   time.Time
	// Note carries a rejection's reason. The person whose week it is needs to
	// know what to fix.
	Note string
	// SubmittedSeconds records the total as it stood at submission, so an
	// approval is a decision about specific figures rather than about whatever
	// the week happens to say now.
	SubmittedSeconds int64
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// Denormalised for the approval queue.
	UserName string
	// CurrentSeconds is the week's total as it stands now. When it differs from
	// SubmittedSeconds, the week changed after being submitted - which a manager
	// should see before approving.
	CurrentSeconds int64
}

// Locked reports whether the week refuses changes.
//
// Submitted and approved both lock. The difference is who can undo it: a
// submission can be withdrawn by its owner, an approval needs a manager.
func (p TimesheetPeriod) Locked() bool {
	return p.Status == PeriodSubmitted || p.Status == PeriodApproved
}

// Editable reports whether the owner may still change the week themselves.
func (p TimesheetPeriod) Editable() bool { return !p.Locked() }

// Changed reports whether the week's total has moved since it was submitted.
//
// It should not be possible - a submitted week is locked - but an administrator
// override or a restored backup could do it, and a manager approving figures
// that have since changed is exactly the situation this flag exists to prevent.
func (p TimesheetPeriod) Changed() bool {
	return p.Status == PeriodSubmitted && p.CurrentSeconds != p.SubmittedSeconds
}

// WeekStartFor returns the first day of the week containing t.
//
// weekStart is an ISO weekday number, 1 = Monday. The result is a date string
// in the same form the period table stores, so callers cannot accidentally key
// a period by an instant.
func WeekStartFor(t time.Time, weekStart int, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	if weekStart < 1 || weekStart > 7 {
		weekStart = 1
	}

	local := t.In(loc)
	// Go counts Sunday as 0; ISO counts it as 7.
	weekday := int(local.Weekday())
	if weekday == 0 {
		weekday = 7
	}

	offset := weekday - weekStart
	if offset < 0 {
		offset += 7
	}
	return local.AddDate(0, 0, -offset).Format("2006-01-02")
}

// ParseWeekStart reads a stored week start back into a date.
func ParseWeekStart(value string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not a week start date", ErrValidation, value)
	}
	return parsed, nil
}

// WeekEnd returns the exclusive end of a week beginning at start.
func WeekEnd(start time.Time) time.Time { return start.AddDate(0, 0, 7) }

// ErrPeriodLocked is returned when a change is refused because the week has been
// submitted or approved.
//
// It is its own error rather than a validation failure, because the caller needs
// to say something specific and actionable: whose approval to ask for, or that
// the submission can simply be withdrawn.
var ErrPeriodLocked = fmt.Errorf("%w: the timesheet for this week is locked", ErrValidation)
