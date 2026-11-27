package service

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/ical"
)

// Importing days away from a calendar.
//
// The same file format as the time import beside it, and deliberately not the
// same code. A calendar read as a timesheet and a calendar read as a leave
// register disagree about every event in it: the time import skips all-day
// events because they have no length, and an all-day event is exactly what a
// day away is. Running both down one function with a flag would mean two sets
// of rules in one body, each guarded by the other's negation.
//
// What this exists for: a national holiday calendar is published as an .ics
// feed by every country that has one, and until now the one category of
// absence that is identical for everybody and already written down was the one
// an administrator typed in by hand, thirteen times a year. The same import
// serves somebody who keeps their own leave in a calendar.
//
// Two passes, as everywhere else that writes in bulk
// (docs/adr/0022-two-pass-csv-import.md): the preview writes nothing.

// HolidayRow is one all-day event, as the day away it would become.
type HolidayRow struct {
	// Index is the event's position in the file, so a message points at
	// something the user can find.
	Index   int
	UID     string
	Summary string
	// StartsOn and EndsOn are the span in the calendar's own dates. A calendar
	// gives the end exclusively - Christmas Eve to Christmas Day means one day
	// - so the end stored here is the last day actually off.
	StartsOn string
	EndsOn   string
	// Days is the length of the span, for the preview's arithmetic.
	Days int
	// Skip explains why this event will not be imported, empty when it will.
	Skip string
	// Duplicate marks a span already recorded. Re-importing next year's feed
	// over this year's is the normal way this is used.
	Duplicate bool
	// Selected is whether the user has asked for this one.
	Selected bool
}

// Importable reports whether this event can become an absence.
func (r HolidayRow) Importable() bool { return r.Skip == "" && !r.Duplicate }

// HolidayPreview is the result of the first pass.
type HolidayPreview struct {
	Rows []HolidayRow
	// Importable, Skipped and Duplicates count the events.
	Importable int
	Skipped    int
	Duplicates int
	// Days is how many days off would be recorded, which is the figure worth
	// checking: a feed with a recurrence rule in it reads as thirteen events
	// and one day.
	Days int
	// Fatal is set when the file could not be read at all.
	Fatal string
}

// HolidayImportResult is what the second pass did.
type HolidayImportResult struct {
	Imported int
	Days     int
	Skipped  []string
}

// ParseCalendarAsAbsence reads an .ics file and works out which events would
// become days away. Nothing is written.
func (s *Service) ParseCalendarAsAbsence(ctx context.Context, r io.Reader, kind domain.AbsenceKind) (HolidayPreview, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return HolidayPreview{}, err
	}
	if !kind.Valid() {
		return HolidayPreview{}, fmt.Errorf("%w: unknown kind of absence", ErrValidation)
	}

	events, err := ical.Parse(r)
	if err != nil {
		return HolidayPreview{Fatal: err.Error()}, nil
	}
	if len(events) == 0 {
		return HolidayPreview{Fatal: "the file contains no events"}, nil
	}

	// Whose register to check for duplicates. A public holiday is the
	// instance's and is stored against nobody, which is how ListAbsences
	// already returns it to everyone.
	owner := actor.ID
	if kind.Everybodys() {
		owner = 0
	}

	preview := HolidayPreview{}
	for i, event := range events {
		row := HolidayRow{Index: i + 1, UID: event.UID, Summary: event.Summary}
		row.StartsOn, row.EndsOn, row.Days = holidaySpan(event)

		switch {
		case event.Cancelled:
			row.Skip = "cancelled"
		case !event.AllDay:
			// A meeting is not a day off. Importing one would record a whole
			// day away because somebody had a two o'clock.
			row.Skip = "not an all-day event, so it is not a day away"
		case event.Recurring:
			// Only the first instance is in the file's expansion, and this
			// application does not expand rules. A holiday feed that states
			// "every 25 December" would otherwise import one Christmas and
			// look like it had imported them all.
			row.Skip = "repeats; only this occurrence is in the file"
		case row.Days <= 0:
			row.Skip = "no dates"
		}

		if row.Skip == "" {
			duplicate, dupErr := s.absenceAlreadyCovers(ctx, owner, row)
			if dupErr != nil {
				return HolidayPreview{}, dupErr
			}
			row.Duplicate = duplicate
		}

		switch {
		case row.Duplicate:
			preview.Duplicates++
		case row.Skip != "":
			preview.Skipped++
		default:
			preview.Importable++
			preview.Days += row.Days
			row.Selected = true
		}
		preview.Rows = append(preview.Rows, row)
	}
	return preview, nil
}

// ImportCalendarAsAbsence records the events the user ticked.
//
// Each one goes through RecordAbsence rather than through SQL of its own, so
// the authorisation is the one already written: a public holiday is refused to
// anybody who may not administer the instance, and it is refused here before a
// single row is written rather than halfway down the file.
func (s *Service) ImportCalendarAsAbsence(ctx context.Context, r io.Reader, kind domain.AbsenceKind, selected map[string]bool) (HolidayImportResult, error) {
	preview, err := s.ParseCalendarAsAbsence(ctx, r, kind)
	if err != nil {
		return HolidayImportResult{}, err
	}
	if preview.Fatal != "" {
		return HolidayImportResult{}, fmt.Errorf("%w: %s", ErrValidation, preview.Fatal)
	}

	result := HolidayImportResult{}
	for _, row := range preview.Rows {
		if !selected[row.UID] {
			continue
		}
		// The preview's own judgement still stands: a meeting does not become
		// a day off because somebody ticked it, and a day already in the
		// register does not go in twice. Both are reported by name - a count
		// alone would leave somebody comparing the feed with the register by
		// eye, which is the thing this import exists to avoid.
		switch {
		case row.Skip != "":
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: %s", row.Summary, row.Skip))
			continue
		case row.Duplicate:
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: %s", row.Summary, alreadyRecorded))
			continue
		}

		_, err := s.RecordAbsence(ctx, AbsenceInput{
			Kind: kind, StartsOn: row.StartsOn, EndsOn: row.EndsOn,
			Note: row.Summary,
		})
		if err != nil {
			// A refusal about one day is reported against that day; anything
			// else is the machinery failing and stops the import, as it does
			// in the bulk path (see isRefusal).
			if !isRefusal(err) {
				return HolidayImportResult{}, err
			}
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: %s", row.Summary, err))
			continue
		}
		result.Imported++
		result.Days += row.Days
	}
	return result, nil
}

// alreadyRecorded is why a day in the feed was not imported again. Not
// translated, because it travels in the same list as the parser's own reasons,
// which are not either; both are shown verbatim.
const alreadyRecorded = "already recorded"

// absenceAlreadyCovers reports whether the register already holds this span.
//
// By the days rather than by the event's identity: a holiday feed re-exported
// next year carries fresh UIDs for the same Christmas, and a public holiday
// somebody typed in by hand has no UID at all. The dates are what matters -
// the same day off recorded twice is the thing to avoid.
func (s *Service) absenceAlreadyCovers(ctx context.Context, userID int64, row HolidayRow) (bool, error) {
	existing, err := s.db.ListAbsences(ctx, userID, row.StartsOn, row.EndsOn)
	if err != nil {
		return false, err
	}
	return len(existing) > 0, nil
}

// holidaySpan reads an event's dates as the days it covers.
//
// A calendar states the end exclusively: a single day off is 25 December to 26
// December, and a week is Monday to the following Monday. Stored here as the
// last day actually off, which is what a person means by "to".
func holidaySpan(event ical.Event) (startsOn, endsOn string, days int) {
	if event.Start.IsZero() {
		return "", "", 0
	}
	const iso = "2006-01-02"
	start := event.Start

	last := start
	if !event.End.IsZero() && event.End.After(start) {
		last = event.End.AddDate(0, 0, -1)
	}
	if last.Before(start) {
		last = start
	}
	days = int(last.Sub(start)/(24*time.Hour)) + 1
	return start.Format(iso), last.Format(iso), days
}
