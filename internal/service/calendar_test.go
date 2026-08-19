package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Importing a calendar.
//
// The parser has its own tests. These are about the judgement: a calendar is
// not a timesheet, and what matters is that the events which are not time
// worked are separated out by name rather than quietly becoming hours.

// calendarFile builds an .ics around a day.
func calendarFile(day time.Time, events string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		strings.ReplaceAll(events, "{DAY}", day.UTC().Format("20060102")) +
		"END:VCALENDAR\r\n"
}

func vevent(uid, summary, start, end, extra string) string {
	return fmt.Sprintf("BEGIN:VEVENT\r\nUID:%s\r\nDTSTART:{DAY}T%sZ\r\nDTEND:{DAY}T%sZ\r\n"+
		"SUMMARY:%s\r\n%sEND:VEVENT\r\n", uid, start, end, summary, extra)
}

// TestCalendarPreviewSeparatesWhatIsNotTime. Every one of these becomes an hour
// somebody did not work if the import is naive, and none of them is noticed
// afterwards.
func TestCalendarPreviewSeparatesWhatIsNotTime(t *testing.T) {
	f := newFixture(t)
	day := f.now

	file := calendarFile(day,
		vevent("real", "Acme Migration Development planning", "080000", "093000", "")+
			vevent("cancelled", "Acme Migration Development sync", "100000", "110000", "STATUS:CANCELLED\r\n")+
			vevent("declined", "Acme Migration Development review", "110000", "120000",
				"ATTENDEE;PARTSTAT=DECLINED;CN=Me:mailto:me@example.com\r\n")+
			vevent("repeats", "Acme Migration Development weekly", "140000", "150000",
				"RRULE:FREQ=WEEKLY\r\n")+
			"BEGIN:VEVENT\r\nUID:allday\r\nDTSTART;VALUE=DATE:{DAY}\r\nDTEND;VALUE=DATE:{DAY}\r\n"+
			"SUMMARY:Public holiday\r\nEND:VEVENT\r\n")

	preview, err := f.svc.ParseCalendar(f.ctx, strings.NewReader(file), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if preview.Fatal != "" {
		t.Fatalf("fatal: %s", preview.Fatal)
	}

	// Every event in the file is accounted for; none is silently dropped.
	if len(preview.Rows) != 5 {
		t.Fatalf("rows = %d, want every event in the file", len(preview.Rows))
	}
	if preview.Importable != 1 {
		t.Errorf("importable = %d, want only the real meeting", preview.Importable)
	}
	if preview.Skipped != 4 {
		t.Errorf("skipped = %d, want the other four", preview.Skipped)
	}

	byUID := map[string]CalendarEventRow{}
	for _, row := range preview.Rows {
		byUID[row.UID] = row
	}
	for uid, wanted := range map[string]string{
		"cancelled": "cancelled",
		"declined":  "declined",
		"repeats":   "repeats",
		"allday":    "all day",
	} {
		// The reason has to be in words, not a count: somebody has to be able
		// to see that the missing hour was a meeting they declined.
		if !strings.Contains(byUID[uid].Skip, wanted) {
			t.Errorf("%s: skip = %q, want it to mention %q", uid, byUID[uid].Skip, wanted)
		}
	}
	if !byUID["real"].Importable() {
		t.Errorf("the real meeting is not importable: %q", byUID["real"].Skip)
	}
	if byUID["real"].Seconds != 5400 {
		t.Errorf("duration = %d, want 5400", byUID["real"].Seconds)
	}
}

// TestCalendarPreviewWritesNothing is the two-pass promise.
func TestCalendarPreviewWritesNothing(t *testing.T) {
	f := newFixture(t)
	file := calendarFile(f.now, vevent("x", "Acme Migration Development", "080000", "090000", ""))

	if _, err := f.svc.ParseCalendar(f.ctx, strings.NewReader(file), 0); err != nil {
		t.Fatalf("preview: %v", err)
	}
	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day.Entries) != 0 {
		t.Errorf("the preview created %d entries", len(day.Entries))
	}
}

// TestCalendarMatchingRefusesToGuess. A meeting on the wrong customer is a
// billing error, so an ambiguous match is reported rather than resolved.
func TestCalendarMatchingRefusesToGuess(t *testing.T) {
	f := newFixture(t)
	// A second assignment in the same project makes "Acme Migration" ambiguous.
	if _, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Support", BillableDefault: true,
	}); err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	file := calendarFile(f.now,
		vevent("ambiguous", "Acme Migration planning", "080000", "090000", "")+
			vevent("specific", "Acme Migration Support call", "100000", "110000", ""))

	preview, err := f.svc.ParseCalendar(f.ctx, strings.NewReader(file), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	byUID := map[string]CalendarEventRow{}
	for _, row := range preview.Rows {
		byUID[row.UID] = row
	}
	if byUID["ambiguous"].AssignmentID != 0 {
		t.Errorf("an ambiguous meeting was assigned to %q", byUID["ambiguous"].AssignmentLabel)
	}
	if !strings.Contains(byUID["ambiguous"].Skip, "no assignment") {
		t.Errorf("the ambiguous meeting says %q", byUID["ambiguous"].Skip)
	}
	// Naming the assignment resolves it.
	if byUID["specific"].AssignmentID == 0 {
		t.Errorf("a meeting naming its assignment did not match: %q", byUID["specific"].Skip)
	}
}

// TestCalendarDefaultAssignmentFillsTheGap: somebody importing a calendar for
// one engagement should not have to pick the same assignment forty times.
func TestCalendarDefaultAssignment(t *testing.T) {
	f := newFixture(t)
	file := calendarFile(f.now, vevent("nomatch", "Something unrelated", "080000", "090000", ""))

	preview, err := f.svc.ParseCalendar(f.ctx, strings.NewReader(file), f.assignment.ID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if preview.Importable != 1 {
		t.Fatalf("importable = %d, want the default to have filled it in", preview.Importable)
	}
	if preview.Rows[0].AssignmentID != f.assignment.ID {
		t.Errorf("assignment = %d, want the default", preview.Rows[0].AssignmentID)
	}
}

// TestCalendarImportIsSafeToRepeat. Exporting again next month and importing
// the overlapping range is the normal way people use this.
func TestCalendarImportIsSafeToRepeat(t *testing.T) {
	f := newFixture(t)
	file := calendarFile(f.now, vevent("x", "Acme Migration Development", "080000", "090000", ""))

	selected := map[string]int64{"x": f.assignment.ID}
	result, err := f.svc.ImportCalendar(f.ctx, strings.NewReader(file), selected, "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 {
		t.Fatalf("created %d, want 1", result.Created)
	}

	// The second preview knows.
	preview, err := f.svc.ParseCalendar(f.ctx, strings.NewReader(file), f.assignment.ID)
	if err != nil {
		t.Fatalf("second preview: %v", err)
	}
	if preview.Duplicates != 1 {
		t.Errorf("duplicates = %d, want the already-imported meeting", preview.Duplicates)
	}
	if preview.Rows[0].Selected {
		t.Error("an already-imported meeting is pre-selected for import")
	}
}

// TestCalendarImportRefusesWhatThePreviewRefused: ticking a cancelled meeting
// does not make it time.
func TestCalendarImportRefusesWhatThePreviewRefused(t *testing.T) {
	f := newFixture(t)
	file := calendarFile(f.now,
		vevent("cancelled", "Acme Migration Development", "080000", "090000", "STATUS:CANCELLED\r\n"))

	result, err := f.svc.ImportCalendar(f.ctx,
		strings.NewReader(file), map[string]int64{"cancelled": f.assignment.ID}, "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 0 {
		t.Errorf("a cancelled meeting was imported")
	}
	if len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0], "cancelled") {
		t.Errorf("skipped = %v, want it to name the cancellation", result.Skipped)
	}
}

// TestCalendarImportReportsPerEventFailures rather than failing the lot: a
// calendar event is one meeting the user individually ticked, so refusing all
// forty because the thirty-first is in a submitted week would be unhelpful.
func TestCalendarImportReportsPerEventFailures(t *testing.T) {
	f := newFixture(t)

	// Last week is submitted and locked; this week is open.
	lastWeek := f.now.AddDate(0, 0, -7)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: lastWeek,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, lastWeek); err != nil {
		t.Fatalf("submit: %v", err)
	}

	file := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		strings.ReplaceAll(vevent("open", "Acme Migration Development now", "080000", "090000", ""),
			"{DAY}", f.now.UTC().Format("20060102")) +
		strings.ReplaceAll(vevent("locked", "Acme Migration Development then", "080000", "090000", ""),
			"{DAY}", lastWeek.UTC().Format("20060102")) +
		"END:VCALENDAR\r\n"

	result, err := f.svc.ImportCalendar(f.ctx, strings.NewReader(file), map[string]int64{
		"open": f.assignment.ID, "locked": f.assignment.ID,
	}, "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("created %d, want the one in the open week", result.Created)
	}
	if len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0], "locked") {
		t.Errorf("skipped = %v, want the locked week named", result.Skipped)
	}
}
