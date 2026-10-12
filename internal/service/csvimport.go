package service

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Bulk import of hours from a CSV file.
//
// The shape of this feature is decided by one thing: an import that partly
// succeeds is worse than one that fails. Someone importing a month of hours from
// a spreadsheet and getting 340 of 400 rows, with no clear account of which
// sixty are missing, has a reconciliation problem rather than a timesheet.
//
// So it is two passes. The first parses and matches everything, reporting every
// problem; nothing is written. The second runs only on the user's confirmation
// and imports every row or none.

// ImportRow is one parsed line, matched against the catalogue.
type ImportRow struct {
	// Line is the row number in the file, counting the header, so an error
	// message points at something the user can find in their spreadsheet.
	Line int

	Date       time.Time
	Seconds    int64
	Customer   string
	Project    string
	Assignment string
	Note       string
	Billable   bool

	// AssignmentID is set when the row matched an existing assignment.
	AssignmentID int64
	// Problem explains why this row cannot be imported, empty when it can.
	Problem string
	// WillCreate lists catalogue records this row would create.
	WillCreate []string
}

// OK reports whether this row can be imported as it stands.
func (r ImportRow) OK() bool { return r.Problem == "" }

// ImportPreview is the result of the first pass.
type ImportPreview struct {
	Rows []ImportRow
	// Valid and Invalid count the rows, so the summary does not require the
	// caller to walk the list.
	Valid   int
	Invalid int
	// TotalSeconds is what would be imported, so a user can sanity-check the
	// figure against their spreadsheet before committing.
	TotalSeconds int64
	// Fatal is set when the file could not be read at all, as opposed to
	// individual rows failing.
	Fatal string
}

// ParseTimeCSV reads a CSV of hours and matches every row against the
// catalogue, without writing anything.
//
// Recognised headers, case-insensitively and with several spellings each,
// because the file usually comes out of somebody else's system:
//
//	date                       required
//	hours | minutes | duration required, in any form ParseDuration accepts
//	customer, project, assignment
//	note | description | comment
//	billable
//
// The duration column's heading decides what a bare number in it means. Under
// "hours", 2 is two hours; under "minutes", 2 is two minutes; under a heading
// that does not say - "duration", "time" - a bare whole number is refused with
// the line number, because eight hours and eight minutes are both plausible
// readings of "8" and picking one silently is how a day of work becomes a
// coffee break. A value that carries its own unit ("45m", "1:30", "1.5") is
// read as written whatever the column is called.
func (s *Service) ParseTimeCSV(ctx context.Context, r io.Reader, createMissing bool) (ImportPreview, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return ImportPreview{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionCreate, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return ImportPreview{}, err
	}

	reader := csv.NewReader(r)
	// Rows may legitimately have different lengths - a trailing empty column is
	// common from spreadsheet exports - so the length check is ours, per row.
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return ImportPreview{Fatal: fmt.Sprintf("could not read the file as CSV: %s", err)}, nil
	}
	if len(records) < 2 {
		return ImportPreview{Fatal: "the file needs a header row and at least one row of data"}, nil
	}

	columns, err := mapImportColumns(records[0])
	if err != nil {
		return ImportPreview{Fatal: err.Error()}, nil
	}

	// The catalogue is loaded once and matched in memory: a per-row query would
	// make a thousand-row import a thousand round trips.
	assignments, err := s.Assignments(ctx, 0, false)
	if err != nil {
		return ImportPreview{}, err
	}

	loc := locationFor(actor)
	preview := ImportPreview{}

	for i, record := range records[1:] {
		row := parseImportRow(record, columns, i+2, loc)

		if row.OK() {
			s.matchImportRow(&row, assignments, createMissing)
		}
		if row.OK() {
			preview.Valid++
			preview.TotalSeconds += row.Seconds
		} else {
			preview.Invalid++
		}
		preview.Rows = append(preview.Rows, row)
	}

	return preview, nil
}

// importColumns records which CSV column holds which field.
type importColumns struct {
	date, duration, customer, project, assignment, note, billable int
	// durationUnit is what the duration column's heading said its numbers are.
	// A file that says "hours" at the top of a column has already told us what
	// a bare 2 in it means.
	durationUnit domain.DurationUnit
}

// mapImportColumns reads the header row.
//
// Several spellings are accepted for each field because the file usually comes
// from another system, and rejecting a file for calling a column "hours"
// instead of "duration" would be needless.
func mapImportColumns(header []string) (importColumns, error) {
	columns := importColumns{
		date: -1, duration: -1, customer: -1, project: -1,
		assignment: -1, note: -1, billable: -1,
	}

	for i, name := range header {
		// Strip a UTF-8 BOM, which Excel writes and which would otherwise make
		// the first column name unmatchable.
		name = strings.TrimPrefix(name, "\ufeff")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "date", "datum", "day":
			columns.date = i
		case "hours", "hour", "hrs", "timmar", "duration_hours", "decimal_hours":
			columns.duration = i
			columns.durationUnit = domain.UnitHours
		case "minutes", "minute", "mins", "minuter":
			columns.duration = i
			columns.durationUnit = domain.UnitMinutes
		case "duration", "time", "varaktighet", "tid":
			columns.duration = i
			columns.durationUnit = domain.UnitUnstated
		case "customer", "client", "kund":
			columns.customer = i
		case "project", "projekt":
			columns.project = i
		case "assignment", "task", "activity", "uppdrag":
			columns.assignment = i
		case "note", "description", "comment", "notes", "anteckning":
			columns.note = i
		case "billable", "debiterbart":
			columns.billable = i
		}
	}

	if columns.date < 0 {
		return columns, fmt.Errorf("no date column found; expected one named date, datum or day")
	}
	if columns.duration < 0 {
		return columns, fmt.Errorf(
			"no duration column found; expected one named hours, minutes, duration or time")
	}
	if columns.assignment < 0 && columns.project < 0 {
		return columns, fmt.Errorf(
			"no assignment or project column found; there is nothing to record the time against")
	}
	return columns, nil
}

// parseImportRow reads one record into a row, without touching the database.
func parseImportRow(record []string, columns importColumns, line int, loc *time.Location) ImportRow {
	row := ImportRow{Line: line, Billable: true}

	field := func(index int) string {
		if index < 0 || index >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[index])
	}

	dateText := field(columns.date)
	parsed, err := parseImportDate(dateText, loc)
	if err != nil {
		row.Problem = fmt.Sprintf("could not read the date %q", dateText)
		return row
	}
	row.Date = parsed

	durationText := field(columns.duration)
	seconds, err := domain.ParseDurationIn(durationText, columns.durationUnit)
	if err != nil {
		if errors.Is(err, domain.ErrAmbiguousDuration) {
			row.Problem = fmt.Sprintf(
				"%q could be hours or minutes; write it as %sh or %sm, or head the "+
					"column hours or minutes", durationText, durationText, durationText)
		} else {
			row.Problem = fmt.Sprintf("could not read the duration %q", durationText)
		}
		return row
	}
	if seconds <= 0 {
		row.Problem = "the duration is zero"
		return row
	}
	row.Seconds = seconds

	row.Customer = field(columns.customer)
	row.Project = field(columns.project)
	row.Assignment = field(columns.assignment)
	row.Note = field(columns.note)

	if columns.billable >= 0 {
		switch strings.ToLower(field(columns.billable)) {
		case "", "1", "true", "yes", "y", "ja", "x":
			row.Billable = true
		default:
			row.Billable = false
		}
	}
	return row
}

// parseImportDate accepts the date formats a spreadsheet is likely to emit.
//
// Deliberately absent: any format where the day and month order is ambiguous.
// 03/04/2026 is either 3 April or 4 March depending on who exported it, and
// guessing would silently put a month of work on the wrong days. A file using
// that format is rejected row by row with a message naming the value, which is
// recoverable; a wrong guess is not.
func parseImportDate(text string, loc *time.Location) (time.Time, error) {
	formats := []string{
		"2006-01-02", // ISO, and the Swedish everyday format
		"2006/01/02",
		"02.01.2006", // unambiguous: four-digit year last, dots
		"2 January 2006",
		"2 Jan 2006",
		"20060102",
	}
	for _, format := range formats {
		if parsed, err := time.ParseInLocation(format, text, loc); err == nil {
			return parsed, nil
		}
	}
	// A timestamp is accepted too; the time part is discarded, since the entry's
	// start comes from elsewhere.
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed.In(loc), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised date format")
}

// matchImportRow resolves a row's assignment against the catalogue.
func (s *Service) matchImportRow(row *ImportRow, assignments []domain.Assignment, createMissing bool) {
	name := row.Assignment
	if name == "" {
		// A file with only a project column records everything against that
		// project's assignment of the same name, which is what such files mean.
		name = row.Project
	}

	var matches []domain.Assignment
	for _, a := range assignments {
		if !strings.EqualFold(a.Name, name) {
			continue
		}
		// When the file also names a customer or project, they must agree -
		// otherwise "Development" matches every client's development work.
		if row.Customer != "" && !strings.EqualFold(a.CustomerName, row.Customer) {
			continue
		}
		if row.Project != "" && row.Assignment != "" && !strings.EqualFold(a.ProjectName, row.Project) {
			continue
		}
		matches = append(matches, a)
	}

	switch {
	case len(matches) == 1:
		row.AssignmentID = matches[0].ID
	case len(matches) > 1:
		row.Problem = fmt.Sprintf("%q matches %d assignments; add a customer column to disambiguate",
			name, len(matches))
	case createMissing:
		// Recorded as intent, not created yet: the second pass does the writing,
		// so a preview never has side effects.
		row.WillCreate = missingCatalogue(row, assignments)
	default:
		row.Problem = fmt.Sprintf("no assignment named %q", name)
	}
}

// missingCatalogue lists what would have to be created for a row.
func missingCatalogue(row *ImportRow, assignments []domain.Assignment) []string {
	customer := row.Customer
	if customer == "" {
		customer = "Imported"
	}
	project := row.Project
	if project == "" {
		project = "Imported"
	}
	assignment := row.Assignment
	if assignment == "" {
		assignment = project
	}

	var missing []string
	hasCustomer, hasProject := false, false
	for _, a := range assignments {
		if strings.EqualFold(a.CustomerName, customer) {
			hasCustomer = true
			if strings.EqualFold(a.ProjectName, project) {
				hasProject = true
			}
		}
	}
	if !hasCustomer {
		missing = append(missing, "customer "+customer)
	}
	if !hasProject {
		missing = append(missing, "project "+project)
	}
	missing = append(missing, "assignment "+assignment)
	return missing
}

// ImportTimeCSV performs the second pass, writing the rows.
//
// It imports every valid row or none: a partial import leaves the user
// reconciling two sources, which is worse than a clear failure. Rows that were
// invalid in the preview are refused outright rather than skipped, so what is
// imported is exactly what was shown.
//
// All-or-nothing is now the transaction rather than the intention. Every row is
// prepared first - which is where a refusal names a line number - and the writes
// then happen together, so a failure part-way through leaves nothing behind.
func (s *Service) ImportTimeCSV(ctx context.Context, r io.Reader, createMissing bool) (int, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return 0, err
	}

	preview, err := s.ParseTimeCSV(ctx, r, createMissing)
	if err != nil {
		return 0, err
	}
	if preview.Fatal != "" {
		return 0, fmt.Errorf("%w: %s", ErrValidation, preview.Fatal)
	}
	if preview.Invalid > 0 {
		return 0, fmt.Errorf("%w: %d of %d rows could not be matched; nothing was imported",
			ErrValidation, preview.Invalid, len(preview.Rows))
	}

	loc := locationFor(actor)

	// The catalogue first, and outside the import's transaction.
	//
	// Creating a customer is itself an audited change in its own transaction,
	// and nesting one inside another on the single write connection would
	// deadlock. It is also the right split: the records created here are what
	// the preview listed and the user agreed to, they are matched by name, so a
	// re-run after a failed import reuses them rather than making a second set.
	// What must not be partial is the time, and that is what the transaction
	// below covers.
	prepared := make([]domain.TimeEntry, 0, len(preview.Rows))
	assignments := make([]domain.Assignment, 0, len(preview.Rows))
	for _, row := range preview.Rows {
		assignmentID := row.AssignmentID
		if assignmentID == 0 {
			if assignmentID, err = s.ensureCatalogue(ctx, row); err != nil {
				return 0, err
			}
		}

		// The work is placed at the start of the working day, because a CSV of
		// daily totals carries no start time. Overlapping entries are legal
		// (docs/adr/0004-concurrent-timers.md), so several imported rows on one
		// day do not conflict - and inventing distinct start times would be
		// fabricating detail the file does not contain.
		started := time.Date(row.Date.Year(), row.Date.Month(), row.Date.Day(), 9, 0, 0, 0, loc)

		entry, assignment, prepErr := s.prepareEntry(ctx, EntryInput{
			AssignmentID:    assignmentID,
			StartedAt:       started,
			DurationSeconds: row.Seconds,
			Note:            row.Note,
			Billable:        row.Billable,
		})
		if prepErr != nil {
			// Nothing has been written, so this is a clean refusal naming the
			// line in the user's spreadsheet.
			return 0, fmt.Errorf("importing row %d: %w", row.Line, prepErr)
		}
		prepared = append(prepared, entry)
		assignments = append(assignments, assignment)
	}

	// Every row, every row's audit record, and the summary, in one transaction.
	//
	// This is what ADR-0022 promised and the row-by-row loop did not deliver:
	// the import either happened or did not. A failure part-way used to leave
	// the earlier rows written, and the summary row - which said how many rows
	// had been imported - was written afterwards in a transaction of its own, so
	// it could fail over an import that had already happened, or claim one that
	// had partly not.
	var imported int
	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		for i, entry := range prepared {
			if _, txErr := s.writeEntryTx(ctx, tx, entry, assignments[i]); txErr != nil {
				return fmt.Errorf("importing row %d: %w", preview.Rows[i].Line, txErr)
			}
			imported++
		}
		return s.audit(ctx, tx, "time_entry.import", "time_entry", 0, 0, map[string]any{
			"rows":    imported,
			"seconds": preview.TotalSeconds,
		})
	})
	if err != nil {
		return 0, err
	}

	s.auditLog(ctx, "time_entry.import", "time_entry", 0)
	return imported, nil
}

// ensureCatalogue creates the customer, project and assignment a row needs.
func (s *Service) ensureCatalogue(ctx context.Context, row ImportRow) (int64, error) {
	customerName := row.Customer
	if customerName == "" {
		customerName = "Imported"
	}
	projectName := row.Project
	if projectName == "" {
		projectName = "Imported"
	}
	assignmentName := row.Assignment
	if assignmentName == "" {
		assignmentName = projectName
	}

	customers, err := s.Customers(ctx, true)
	if err != nil {
		return 0, err
	}
	var customerID int64
	for _, c := range customers {
		if strings.EqualFold(c.Name, customerName) {
			customerID = c.ID
			break
		}
	}
	if customerID == 0 {
		created, err := s.CreateCustomer(ctx, domain.Customer{Name: customerName})
		if err != nil {
			return 0, err
		}
		customerID = created.ID
	}

	projects, err := s.Projects(ctx, customerID, true)
	if err != nil {
		return 0, err
	}
	var projectID int64
	for _, p := range projects {
		if strings.EqualFold(p.Name, projectName) {
			projectID = p.ID
			break
		}
	}
	if projectID == 0 {
		created, err := s.CreateProject(ctx, domain.Project{
			CustomerID: customerID, Name: projectName, BillableDefault: true,
		})
		if err != nil {
			return 0, err
		}
		projectID = created.ID
	}

	assignments, err := s.Assignments(ctx, projectID, true)
	if err != nil {
		return 0, err
	}
	for _, a := range assignments {
		if strings.EqualFold(a.Name, assignmentName) {
			return a.ID, nil
		}
	}
	created, err := s.CreateAssignment(ctx, domain.Assignment{
		ProjectID: projectID, Name: assignmentName, BillableDefault: true,
	})
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}
