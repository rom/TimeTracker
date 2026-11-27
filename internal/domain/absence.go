package domain

import (
	"fmt"
	"time"
)

// A day somebody was not working.
//
// Deliberately not a kind of time entry. An entry needs an assignment, which
// needs a project, which needs a customer, so recording a holiday as time would
// put a fake customer in every instance's catalogue and leave would turn up in
// reports grouped by customer. Absence is about a person and a date and nothing
// else.

// AbsenceKind is why somebody was away.
type AbsenceKind string

const (
	// AbsenceHoliday is annual leave, taken by the person.
	AbsenceHoliday AbsenceKind = "holiday"
	// AbsenceSick is sick leave.
	AbsenceSick AbsenceKind = "sick"
	// AbsencePublic is a public holiday: the instance's, applying to
	// everybody, recorded once by an administrator.
	AbsencePublic AbsenceKind = "public"
	// AbsenceParental covers parental and other statutory leave.
	AbsenceParental AbsenceKind = "parental"
	// AbsenceOther is anything else somebody needs to record, so that the list
	// above does not have to anticipate every arrangement.
	AbsenceOther AbsenceKind = "other"
)

// AbsenceKinds are the kinds offered, in the order a picker should show them.
var AbsenceKinds = []AbsenceKind{
	AbsenceHoliday, AbsenceSick, AbsencePublic, AbsenceParental, AbsenceOther,
}

// MessageKey names the translated label for a kind.
func (k AbsenceKind) MessageKey() string { return "absence.kind." + string(k) }

// Valid reports whether a kind is one of the known ones.
func (k AbsenceKind) Valid() bool {
	for _, known := range AbsenceKinds {
		if k == known {
			return true
		}
	}
	return false
}

// Everybodys reports whether a kind belongs to the instance rather than to one
// person. Only a public holiday does.
func (k AbsenceKind) Everybodys() bool { return k == AbsencePublic }

// Absence is a span of days somebody was away, inclusive at both ends.
type Absence struct {
	ID int64
	// UserID is zero for a public holiday, which is everybody's.
	UserID    int64
	Kind      AbsenceKind
	StartsOn  string // YYYY-MM-DD
	EndsOn    string // YYYY-MM-DD
	Note      string
	CreatedBy int64
	CreatedAt time.Time

	// Denormalised for the screens.
	UserName string
}

// Validate checks an absence before it is stored.
func (a Absence) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: %q is not a kind of absence", ErrValidation, a.Kind)
	}
	start, err := time.Parse("2006-01-02", a.StartsOn)
	if err != nil {
		return fmt.Errorf("%w: %q is not a date", ErrValidation, a.StartsOn)
	}
	end, err := time.Parse("2006-01-02", a.EndsOn)
	if err != nil {
		return fmt.Errorf("%w: %q is not a date", ErrValidation, a.EndsOn)
	}
	if end.Before(start) {
		return fmt.Errorf("%w: an absence cannot end before it begins", ErrValidation)
	}
	// A year is not a limit anybody will meet by accident, and it stops a
	// mistyped year from covering every day the instance will ever have.
	if end.Sub(start) > 366*24*time.Hour {
		return fmt.Errorf("%w: an absence of more than a year is probably a typo",
			ErrValidation)
	}
	return nil
}

// Covers reports whether a day falls inside the absence.
//
// String comparison on YYYY-MM-DD, which sorts the same way the dates do, and
// avoids turning a calendar day back into an instant in order to compare it -
// which is where a day off would start drifting across midnight.
func (a Absence) Covers(day string) bool {
	return day >= a.StartsOn && day <= a.EndsOn
}

// Days lists the calendar days an absence covers.
func (a Absence) Days() []string {
	start, err := time.Parse("2006-01-02", a.StartsOn)
	if err != nil {
		return nil
	}
	end, err := time.Parse("2006-01-02", a.EndsOn)
	if err != nil {
		return nil
	}
	var days []string
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		days = append(days, day.Format("2006-01-02"))
	}
	return days
}

// AbsentDays reduces a set of absences to the days they cover, keyed by date.
//
// One map rather than a list, because every caller is asking the same question
// of a particular day: the week grid about each of seven, the expected-hours
// arithmetic about each working day, the reminder about the day it is nagging
// over. Where two absences cover the same day - a sick day inside a holiday -
// the first one wins, which only affects which label is shown.
func AbsentDays(absences []Absence) map[string]Absence {
	days := map[string]Absence{}
	for _, absence := range absences {
		for _, day := range absence.Days() {
			if _, taken := days[day]; !taken {
				days[day] = absence
			}
		}
	}
	return days
}
