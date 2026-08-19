package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A routine is a template for time that recurs: lunch, the stand-up, Friday
// admin, the weekly project meeting.
//
// It is offered, never fired. Nothing is created until a person says so, on the
// day. Generating billable hours because the calendar said Tuesday would put
// time on an invoice that nobody worked, and the first to notice would be the
// client - see docs/adr/0027-routines-are-offered-not-fired.md.

// Routine is one recurring template.
type Routine struct {
	ID           int64
	UserID       int64
	AssignmentID int64
	Name         string
	Note         string
	// Weekdays are ISO weekday numbers, 1 = Monday, 7 = Sunday.
	Weekdays []int
	// StartTime is "HH:MM", or empty for "no particular time".
	StartTime       string
	DurationSeconds int64
	Billable        bool
	Kind            EntryKind
	Tags            []string
	Active          bool
	SortOrder       int
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// Denormalised for display.
	AssignmentName string
	ProjectName    string
	CustomerName   string
	ColourKey      string
	Icon           string
}

// AppliesOn reports whether the routine is due on a day.
func (r Routine) AppliesOn(day time.Time) bool {
	if !r.Active {
		return false
	}
	weekday := int(day.Weekday())
	if weekday == 0 {
		// Go counts Sunday as 0; ISO counts it as 7, and so does the stored
		// list. Getting this wrong offers Monday's routines on Sunday.
		weekday = 7
	}
	for _, allowed := range r.Weekdays {
		if allowed == weekday {
			return true
		}
	}
	return false
}

// Validate checks the rules that hold regardless of storage.
func (r Routine) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return invalid("a routine needs a name")
	}
	if len(r.Name) > 120 {
		return invalid("routine name is too long (max 120 characters)")
	}
	if r.AssignmentID == 0 {
		return invalid("a routine must be on an assignment")
	}
	if len(r.Weekdays) == 0 {
		return invalid("a routine needs at least one day of the week")
	}
	for _, day := range r.Weekdays {
		if day < 1 || day > 7 {
			return invalid("%d is not a day of the week", day)
		}
	}
	if r.DurationSeconds <= 0 {
		return invalid("a routine needs a length")
	}
	if r.DurationSeconds > maxEntrySeconds {
		return invalid("a routine cannot be longer than %d days", maxEntrySeconds/86400)
	}
	if r.StartTime != "" {
		if _, err := time.Parse("15:04", r.StartTime); err != nil {
			return invalid("%q is not a time of day", r.StartTime)
		}
	}
	if r.Kind != "" && !r.Kind.Valid() {
		return invalid("unknown kind of time %q", r.Kind)
	}
	return nil
}

// FormatWeekdays renders the stored list, for the database and for backups.
func FormatWeekdays(days []int) string {
	parts := make([]string, 0, len(days))
	for _, day := range days {
		parts = append(parts, strconv.Itoa(day))
	}
	return strings.Join(parts, ",")
}

// ParseWeekdays reads the stored list back, sorted and de-duplicated so that
// "5,1,1" and "1,5" are the same routine.
func ParseWeekdays(raw string) []int {
	seen := map[int]bool{}
	var days []int
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 1 || value > 7 || seen[value] {
			continue
		}
		seen[value] = true
		days = append(days, value)
	}
	sort.Ints(days)
	return days
}

// WeekdayNames is the ISO order, for a form that offers the days.
func WeekdayNames() []struct {
	Number int
	Key    string
} {
	names := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	out := make([]struct {
		Number int
		Key    string
	}, len(names))
	for i, name := range names {
		out[i].Number = i + 1
		out[i].Key = "weekday." + name
	}
	return out
}

// Describe renders a routine as a sentence, for a confirmation or a list.
func (r Routine) Describe() string {
	return fmt.Sprintf("%s, %s", r.Name, FormatDuration(r.DurationSeconds))
}
