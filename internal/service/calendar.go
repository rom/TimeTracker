package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/ical"
)

// Importing a calendar.
//
// Two passes, like the CSV import (docs/adr/0022-two-pass-csv-import.md) and
// for the same reason: the first shows every event and what it would become and
// writes nothing; the second imports on confirmation.
//
// Calendars need that discipline more than spreadsheets do, because a
// calendar is *not* a timesheet. It contains meetings that were cancelled,
// meetings the person declined, all-day markers for public holidays, and
// blocks somebody put in to protect an afternoon. Importing a calendar wholesale
// produces a week that looks plausible and is wrong, and the errors are exactly
// the ones nobody spots afterwards - which is why every event that will not be
// imported says so, by name, in the preview.

// CalendarEventRow is one event, matched against the catalogue.
type CalendarEventRow struct {
	// Index is the event's position in the file, so a message points at
	// something the user can find.
	Index   int
	UID     string
	Summary string
	Start   time.Time
	Seconds int64

	// AssignmentID is set when the event matched an assignment.
	AssignmentID int64
	// AssignmentLabel is what it matched, for the preview.
	AssignmentLabel string
	// Skip explains why this event will not be imported, empty when it will.
	Skip string
	// Duplicate marks an event already imported. Re-importing an overlapping
	// export is the normal way people use this - they export again next month
	// and the ranges overlap - so it has to be safe.
	Duplicate bool
	// Selected is whether the user has asked for this one. The preview
	// pre-selects what looks importable and the user corrects it.
	Selected bool
}

// Importable reports whether this event can become an entry.
func (r CalendarEventRow) Importable() bool { return r.Skip == "" && !r.Duplicate }

// CalendarPreview is the result of the first pass.
type CalendarPreview struct {
	Rows []CalendarEventRow
	// Importable and Skipped count the events.
	Importable int
	Skipped    int
	Duplicates int
	// TotalSeconds is what would be imported if everything selected were taken,
	// so the figure can be checked against the week before committing.
	TotalSeconds int64
	// Fatal is set when the file could not be read at all.
	Fatal string
}

// ParseCalendar reads an .ics file and works out what each event would become.
//
// Nothing is written. Matching is by name against the assignment catalogue,
// looking at the summary and then the location: a meeting called "Acme
// migration sync" finds the Acme migration assignment. An event that matches
// nothing is offered with no assignment, and the user picks one - it is not
// guessed, because a meeting on the wrong customer is a billing error.
func (s *Service) ParseCalendar(ctx context.Context, r io.Reader, defaultAssignmentID int64) (CalendarPreview, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return CalendarPreview{}, err
	}

	events, err := ical.Parse(r)
	if err != nil {
		return CalendarPreview{Fatal: err.Error()}, nil
	}
	if len(events) == 0 {
		return CalendarPreview{Fatal: "the file contains no events"}, nil
	}

	assignments, err := s.Assignments(ctx, 0, false)
	if err != nil {
		return CalendarPreview{}, err
	}
	loc := locationFor(actor)

	preview := CalendarPreview{}
	for i, event := range events {
		row := CalendarEventRow{
			Index:   i + 1,
			UID:     event.UID,
			Summary: event.Summary,
			Start:   event.Start.In(loc),
			Seconds: int64(event.Duration() / time.Second),
		}

		// Everything that makes an event unsuitable is named rather than
		// silently dropped, so the preview accounts for every line in the file.
		switch {
		case event.Cancelled:
			row.Skip = "cancelled"
		case event.Declined:
			row.Skip = "you declined this"
		case event.AllDay:
			row.Skip = "all day, so it has no length"
		case row.Seconds <= 0:
			row.Skip = "no length"
		case event.Recurring:
			// Only the first instance is in the file's expansion, and this
			// application does not expand rules. Importing the one instance
			// silently would look like a bug the first time somebody compared
			// the import with their calendar.
			row.Skip = "repeats; only this occurrence is in the file"
		}

		if row.Importable() {
			row.AssignmentID, row.AssignmentLabel = matchCalendarAssignment(event, assignments)
			if row.AssignmentID == 0 && defaultAssignmentID != 0 {
				row.AssignmentID = defaultAssignmentID
				for _, assignment := range assignments {
					if assignment.ID == defaultAssignmentID {
						row.AssignmentLabel = assignment.Label()
						break
					}
				}
			}
			if row.AssignmentID == 0 {
				row.Skip = "no assignment matched; choose one below"
			}
		}

		if row.Importable() {
			duplicate, dupErr := s.calendarDuplicate(ctx, actor.ID, row)
			if dupErr != nil {
				return CalendarPreview{}, dupErr
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
			preview.TotalSeconds += row.Seconds
			row.Selected = true
		}
		preview.Rows = append(preview.Rows, row)
	}
	return preview, nil
}

// matchCalendarAssignment finds the assignment an event's words point at.
//
// A meeting is named after the work, not after the catalogue: "Acme migration
// planning" names a customer and a project but no assignment, while "Acme
// migration support call" names all three. Both have to work, and they need
// different answers.
//
// So matching happens in two tiers, most specific first:
//
//  1. the event names an assignment - "support" appears somewhere in it,
//     possibly qualified by the customer or project;
//  2. failing that, the event names a project.
//
// The first tier that yields exactly one candidate wins. Without the tiers,
// "Acme migration support call" would match Support *and* every other
// assignment under Migration, and the tie would throw away a match the words
// plainly made.
//
// A tie within a tier returns nothing rather than picking. If "Acme migration"
// names a project with two assignments, which of them the meeting belongs to is
// genuinely unknown, and guessing is guessing whose invoice an hour lands on.
// The preview then asks, which is one dropdown.
func matchCalendarAssignment(event ical.Event, assignments []domain.Assignment) (int64, string) {
	haystack := strings.ToLower(strings.Join(
		[]string{event.Summary, event.Location, event.Description}, " "))

	// Tier 1: forms that name the assignment itself. Listed separately rather
	// than concatenated so that "every word must appear" stays meaningful - one
	// long string would demand the event mention customer, project and
	// assignment together.
	byAssignment := func(a domain.Assignment) []string {
		return []string{
			a.CustomerName + " " + a.ProjectName + " " + a.Name,
			a.CustomerName + " " + a.Name,
			a.ProjectName + " " + a.Name,
			a.Name,
		}
	}
	// Tier 2: forms that name only the project.
	byProject := func(a domain.Assignment) []string {
		return []string{
			a.CustomerName + " " + a.ProjectName,
			a.ProjectName,
		}
	}

	for _, forms := range []func(domain.Assignment) []string{byAssignment, byProject} {
		var matchedID int64
		var matchedLabel string
		matches := 0

		for _, assignment := range assignments {
			if assignment.Archived() {
				continue
			}
			for _, candidate := range forms(assignment) {
				if mentionsAll(haystack, candidate) {
					matches++
					matchedID, matchedLabel = assignment.ID, assignment.Label()
					break
				}
			}
		}
		if matches == 1 {
			return matchedID, matchedLabel
		}
	}
	return 0, ""
}

// mentionsAll reports whether every word of a phrase appears in the text.
func mentionsAll(haystack, phrase string) bool {
	words := strings.Fields(strings.ToLower(phrase))
	if len(words) == 0 {
		return false
	}
	for _, word := range words {
		if len(word) < 3 {
			// Ignore very short words: "AB" or "of" in a customer name would
			// match almost any meeting.
			continue
		}
		if !strings.Contains(haystack, word) {
			return false
		}
	}
	return true
}

// calendarDuplicate reports whether an event has already been imported.
//
// Matched on the day and the exact start instant and length, because the UID is
// not stored on the entry: an entry is time somebody worked, not a reference to
// a calendar it happened to come from, and putting a foreign system's
// identifier on it would be inviting the calendar to own the timesheet.
//
// Re-importing an overlapping export is the normal way people use this - they
// export again next month and the ranges overlap - so it has to be safe.
func (s *Service) calendarDuplicate(ctx context.Context, userID int64, row CalendarEventRow) (bool, error) {
	loc := row.Start.Location()
	existing, err := s.rangeEntries(ctx, userID,
		startOfDay(row.Start, loc), startOfDay(row.Start, loc).AddDate(0, 0, 1))
	if err != nil {
		return false, err
	}
	for _, entry := range existing {
		if entry.AssignmentID == row.AssignmentID &&
			entry.DurationSeconds == row.Seconds &&
			entry.StartedAt.Equal(row.Start.UTC()) {
			return true, nil
		}
	}
	return false, nil
}

// ImportCalendar writes the events the user selected.
//
// Unlike the CSV import this is not all-or-nothing, because the unit is
// different: a CSV row is a line of a document the user is importing whole,
// while a calendar event is one meeting they have individually ticked. Refusing
// all forty because the thirty-first lands in a submitted week would be
// unhelpful, so each is reported.
func (s *Service) ImportCalendar(ctx context.Context, r io.Reader, selected map[string]int64, note string) (CalendarImportResult, error) {
	preview, err := s.ParseCalendar(ctx, r, 0)
	if err != nil {
		return CalendarImportResult{}, err
	}
	if preview.Fatal != "" {
		return CalendarImportResult{}, fmt.Errorf("%w: %s", ErrValidation, preview.Fatal)
	}

	// Deciding first, writing second.
	//
	// The per-meeting tolerance this import is built around happens here, where
	// nothing has been written yet: a cancelled meeting, a locked week, an
	// assignment the actor may not use are all refused by prepareEntry, and each
	// is reported against the meeting it concerns. What is left is a list of
	// entries that will write, and those go in together - so the audit rows and
	// the entries commit as one, and a database failure part-way through does
	// not leave a half-imported calendar with a summary claiming otherwise.
	result := CalendarImportResult{}
	prepared := make([]domain.TimeEntry, 0, len(preview.Rows))
	assignments := make([]domain.Assignment, 0, len(preview.Rows))
	var seconds int64

	for _, row := range preview.Rows {
		assignmentID, wanted := selected[row.UID]
		if !wanted || assignmentID == 0 {
			continue
		}
		// The preview's own refusals still stand: a cancelled meeting does not
		// become time because somebody ticked it.
		if row.Skip != "" && !strings.HasPrefix(row.Skip, "no assignment") {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: %s", row.Summary, row.Skip))
			continue
		}

		entryNote := row.Summary
		if note != "" {
			entryNote = strings.TrimSpace(note + " " + row.Summary)
		}
		entry, assignment, prepErr := s.prepareEntry(ctx, EntryInput{
			AssignmentID:    assignmentID,
			StartedAt:       row.Start,
			DurationSeconds: row.Seconds,
			Note:            entryNote,
			Billable:        true,
		})
		if prepErr != nil {
			// A locked week, an archived assignment: reported against the
			// meeting it concerns rather than failing the whole import.
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: %s", row.Summary, prepErr.Error()))
			continue
		}
		prepared = append(prepared, entry)
		assignments = append(assignments, assignment)
		seconds += row.Seconds
	}

	if len(prepared) == 0 {
		// Nothing to write, so nothing to record. An audit row saying an import
		// created nothing is noise in the one table that should be all signal.
		return result, nil
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		created := 0
		for i, entry := range prepared {
			if _, txErr := s.writeEntryTx(ctx, tx, entry, assignments[i]); txErr != nil {
				return txErr
			}
			created++
		}
		return s.audit(ctx, tx, "calendar.import", "time_entry", 0, 0, map[string]any{
			"created": created,
			"seconds": seconds,
			"skipped": len(result.Skipped),
		})
	})
	if err != nil {
		return CalendarImportResult{}, err
	}

	result.Created = len(prepared)
	result.Seconds = seconds
	s.auditLog(ctx, "calendar.import", "time_entry", 0)
	return result, nil
}

// CalendarImportResult reports what was written and what was not.
type CalendarImportResult struct {
	Created int
	Seconds int64
	// Skipped names each event that was asked for and could not be imported,
	// with the reason. A count alone would leave somebody comparing their
	// calendar with their timesheet by eye.
	Skipped []string
}
