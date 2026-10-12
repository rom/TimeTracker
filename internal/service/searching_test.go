package service

import (
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The searches and listings behind the entries screen, and the handful of
// writes whose less common branch had no test.

// TestNarrowingToDaysThatHadCostsOnThem.
//
// "Time that had costs attached to it" is what the control says, and an
// expense hangs off a project and a date rather than off an entry - so this is
// the closest true statement to it. The comparison is deliberately not a SQL
// condition: an entry's calendar day comes from the zone it was recorded in,
// and date(started_at) would give the UTC day, putting an evening entry in
// Stockholm on the wrong side of midnight.
func TestNarrowingToDaysThatHadCostsOnThem(t *testing.T) {
	f := newFixture(t)

	withCost := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	withoutCost := time.Date(2026, 3, 18, 9, 0, 0, 0, time.UTC)

	onCostDay := mustCreate(t, f, withCost, 3600)
	mustCreate(t, f, withoutCost, 3600)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi to the client", Amount: "100", Billable: true,
	}); err != nil {
		t.Fatalf("record an expense: %v", err)
	}

	filter := EntryFilter{
		From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}

	all, err := f.svc.Entries(f.ctx, filter)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("%d entries in March, want 2", len(all))
	}

	filter.WithExpenses = true
	narrowed, _, err := f.svc.SearchEntries(f.ctx, filter)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(narrowed) != 1 {
		t.Fatalf("%d entries on days with costs, want 1", len(narrowed))
	}
	if narrowed[0].ID != onCostDay.ID {
		t.Errorf("kept entry %d, want the one on the day of the expense %d",
			narrowed[0].ID, onCostDay.ID)
	}

	// The paged form narrows the same way, and reports a total that matches
	// what it kept rather than what the query found before narrowing - a pager
	// saying "1-1 of 2" over one row is worse than no pager.
	filter.Limit = 50
	page, err := f.svc.SearchEntriesPage(f.ctx, filter)
	if err != nil {
		t.Fatalf("paged search: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Errorf("the page holds %d entries, want 1", len(page.Entries))
	}
	if page.Total != 1 {
		t.Errorf("the page reports a total of %d, want 1 - it must count what it "+
			"kept, not what the query found before narrowing", page.Total)
	}

	// An expense on another project does not vouch for this one's day.
	other, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: project.CustomerID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create another project: %v", err)
	}
	if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: other.ID, SpentOn: "2026-03-18", Category: "travel",
		Description: "Taxi elsewhere", Amount: "100", Billable: true,
	}); err != nil {
		t.Fatalf("record an expense elsewhere: %v", err)
	}
	stillOne, _, err := f.svc.SearchEntries(f.ctx, EntryFilter{
		From: filter.From, To: filter.To, WithExpenses: true,
	})
	if err != nil {
		t.Fatalf("re-search: %v", err)
	}
	if len(stillOne) != 1 {
		t.Errorf("%d entries kept, want 1 - an expense on another project is not "+
			"a cost attached to this one's day", len(stillOne))
	}

	// And a range with no entries in it narrows to nothing without a query for
	// the expenses, which is the early return.
	empty, _, err := f.svc.SearchEntries(f.ctx, EntryFilter{
		From:         time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		To:           time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC),
		WithExpenses: true,
	})
	if err != nil {
		t.Fatalf("search an empty range: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("%d entries in a range with none in it", len(empty))
	}
}

// TestStoppingSeveralTimersAtOnce.
//
// The end of the day, with more than one timer running - which happens when
// somebody starts one on their laptop and another on their desktop. Each has
// to be stopped and counted, and the count is what the screen reports back.
func TestStoppingSeveralTimersAtOnce(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// Nothing running: stopping everything is a no-op that says so.
	if stopped, err := f.svc.StopAllTimers(f.ctx); err != nil || stopped != 0 {
		t.Errorf("stopping nothing = %d, %v; want 0 and no error", stopped, err)
	}

	for _, assignment := range []domain.Assignment{f.assignment, second} {
		if _, err := f.svc.StartTimer(f.ctx, assignment.ID, ""); err != nil {
			t.Fatalf("start on %q: %v", assignment.Name, err)
		}
	}
	running, err := f.svc.RunningTimers(f.ctx)
	if err != nil {
		t.Fatalf("read running timers: %v", err)
	}
	if len(running) != 2 {
		t.Fatalf("%d timers running, want 2", len(running))
	}

	f.now = f.now.Add(90 * time.Minute)
	stopped, err := f.svc.StopAllTimers(f.ctx)
	if err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if stopped != 2 {
		t.Errorf("stopped %d timers, want 2", stopped)
	}
	if after, err := f.svc.RunningTimers(f.ctx); err != nil {
		t.Fatalf("re-read running timers: %v", err)
	} else if len(after) != 0 {
		t.Errorf("%d timers still running after stopping everything", len(after))
	}

	// Both were given the time they ran for, rather than one of them being
	// closed at zero.
	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("read the day: %v", err)
	}
	for _, entry := range day.Entries {
		if entry.DurationSeconds != 90*60 {
			t.Errorf("a stopped timer recorded %ds, want 90 minutes", entry.DurationSeconds)
		}
	}
}

// TestAnEntryWithNeitherAnEndNorALengthKeepsRunning.
//
// Editing a running timer - to fix its assignment or its note - must not stop
// it. Giving it an end would be a decision the person did not make, and
// recording zero would lose the morning.
func TestAnEntryWithNeitherAnEndNorALengthKeepsRunning(t *testing.T) {
	f := newFixture(t)

	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "First guess")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	f.now = f.now.Add(time.Hour)
	edited, err := f.svc.UpdateEntry(f.ctx, started.ID, EntryInput{
		AssignmentID: f.assignment.ID,
		StartedAt:    started.StartedAt,
		Note:         "What it actually was",
		Billable:     true,
	})
	if err != nil {
		t.Fatalf("edit the running timer: %v", err)
	}
	if edited.EndedAt != nil {
		t.Errorf("editing a running timer stopped it at %s", edited.EndedAt)
	}
	if edited.DurationSeconds != 0 {
		t.Errorf("the running timer was given a length of %ds", edited.DurationSeconds)
	}
	if edited.Note != "What it actually was" {
		t.Errorf("the edit did not take: note = %q", edited.Note)
	}

	running, err := f.svc.RunningTimers(f.ctx)
	if err != nil {
		t.Fatalf("read running timers: %v", err)
	}
	if len(running) != 1 {
		t.Errorf("%d timers running after editing one, want 1", len(running))
	}
}

// TestDecidingAboutACostSomebodyElseRecorded.
//
// An expense recorded on your behalf is a claim about money, so it waits for
// you the same way a time entry does. Accepting makes it count; declining
// keeps the record of what was claimed, because a claim that vanished would
// leave its author wondering whether it was ever seen.
func TestDecidingAboutACostSomebodyElseRecorded(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	project, err := f.svc.Project(f.ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	propose := func(t *testing.T, description string) domain.Expense {
		t.Helper()
		expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
			ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
			Description: description, Amount: "100", Billable: true,
			OnBehalfOf: colleague.ID,
		})
		if err != nil {
			t.Fatalf("propose %q: %v", description, err)
		}
		if expense.Status != domain.StatusPending {
			t.Fatalf("a proposed expense has status %q, want pending", expense.Status)
		}
		return expense
	}

	accepted := propose(t, "Taxi they took")
	declined := propose(t, "Taxi they did not take")

	// Only the person whose expense it is may decide.
	theirs := f.asUser(colleague)
	if err := f.accounts.AddMember(f.ctx, Membership{
		ProjectID: project.ID, UserID: colleague.ID,
	}); err != nil && !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("ensure membership: %v", err)
	}

	if err := f.svc.AcceptExpense(theirs, accepted.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := f.svc.RejectExpense(theirs, declined.ID, "I walked"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	after, err := f.svc.Expense(theirs, accepted.ID)
	if err != nil {
		t.Fatalf("read the accepted expense: %v", err)
	}
	if after.Status != domain.StatusConfirmed {
		t.Errorf("the accepted expense's status is %q, want confirmed", after.Status)
	}

	refused, err := f.svc.Expense(theirs, declined.ID)
	if err != nil {
		t.Fatalf("read the declined expense: %v", err)
	}
	if refused.Status != domain.StatusRejected {
		t.Errorf("the declined expense's status is %q, want rejected - the record "+
			"of what was claimed is kept rather than deleted", refused.Status)
	}

	// The author of the claim cannot decide it themselves.
	another := propose(t, "Taxi nobody has decided")
	if err := f.svc.AcceptExpense(f.ctx, another.ID); err == nil {
		t.Error("the person who recorded a claim on somebody else's behalf accepted it themselves")
	}
}

// TestARoutineIsEditedRatherThanDuplicated.
//
// Saving with an id edits; saving without one creates. If an edit created, the
// routines screen would fill up with near-copies and applying them all would
// record the same quarter-hour several times.
func TestARoutineIsEditedRatherThanDuplicated(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.SaveRoutine(f.ctx, domain.Routine{
		AssignmentID: f.assignment.ID, Name: "Standup",
		Weekdays: []int{1, 2, 3, 4, 5}, DurationSeconds: 900, Active: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	routines, err := f.svc.Routines(f.ctx, false)
	if err != nil || len(routines) != 1 {
		t.Fatalf("%d routines after creating one: %v", len(routines), err)
	}

	edited := routines[0]
	edited.Name = "Standup (short)"
	edited.DurationSeconds = 600
	if err := f.svc.SaveRoutine(f.ctx, edited); err != nil {
		t.Fatalf("edit: %v", err)
	}

	after, err := f.svc.Routines(f.ctx, false)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("%d routines after an edit, want 1 - an edit that created would "+
			"fill the screen with near-copies", len(after))
	}
	if after[0].Name != "Standup (short)" || after[0].DurationSeconds != 600 {
		t.Errorf("the edit did not take: %+v", after[0])
	}

	// Saving an id that does not exist is a refusal rather than a quiet
	// creation under a different id.
	ghost := edited
	ghost.ID = 9999
	if err := f.svc.SaveRoutine(f.ctx, ghost); err == nil {
		t.Error("saving a routine that does not exist was accepted")
	}
}

// TestCopyingADaySkipsWhatCanNoLongerBeRecorded.
//
// A day being copied forward may hold work against an assignment that has
// since been archived, or a timer that is still running. Copying either would
// be wrong in a different way - the first records time against something taken
// off every form, the second either invents a length or starts a second timer
// nobody asked for - so both are skipped and counted, and the screen can say
// why it copied less than the day holds.
func TestCopyingADaySkipsWhatCanNoLongerBeRecorded(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	stale, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Old work", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// A source day with three things on it: ordinary work, work against what
	// will be archived, and a timer still running.
	source := f.now
	mustCreate(t, f, at(source, 9, 0), 3600)

	staleEnd := at(source, 12, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: stale.ID, StartedAt: at(source, 11, 0), EndedAt: &staleEnd,
		Billable: true,
	}); err != nil {
		t.Fatalf("record against the assignment to be archived: %v", err)
	}
	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "still going"); err != nil {
		t.Fatalf("start a timer: %v", err)
	}

	if err := f.svc.SetAssignmentArchived(f.ctx, stale.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	target := source.AddDate(0, 0, 1)
	result, err := f.svc.CopyDay(f.ctx, source, target)
	if err != nil {
		t.Fatalf("copy the day: %v", err)
	}

	if result.Created != 1 {
		t.Errorf("copied %d entries, want the 1 that can still be recorded", result.Created)
	}
	if result.SkippedArchived != 1 {
		t.Errorf("skipped %d entries on archived assignments, want 1 - copying one "+
			"would record time against something taken off every form",
			result.SkippedArchived)
	}
	if result.SkippedRunning != 1 {
		t.Errorf("skipped %d running timers, want 1 - copying one would either "+
			"invent a length or start a second timer", result.SkippedRunning)
	}

	day, err := f.svc.Day(f.ctx, target)
	if err != nil {
		t.Fatalf("read the target day: %v", err)
	}
	if len(day.Entries) != 1 {
		t.Errorf("the copied day holds %d entries, want 1", len(day.Entries))
	}
}

// TestArchivingAndRestoringAreDifferentActionsInTheTrail.
//
// Both are the same column changing, and they are not the same event. Somebody
// reading the trail months later needs to see which way it went, because "this
// was archived" and "this was brought back" answer different questions.
func TestArchivingAndRestoringAreDifferentActionsInTheTrail(t *testing.T) {
	f := newFixture(t)

	for _, archived := range []bool{true, false} {
		if err := f.svc.SetAssignmentArchived(f.ctx, f.assignment.ID, archived); err != nil {
			t.Fatalf("set archived=%v: %v", archived, err)
		}
	}

	trail, err := f.svc.AuditTrail(f.ctx, "assignment", f.assignment.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}

	actions := map[string]bool{}
	for _, event := range trail {
		actions[event.Action] = true
	}
	for _, want := range []string{"assignment.archive", "assignment.restore"} {
		if !actions[want] {
			t.Errorf("the trail has no %q event; it holds %v", want, actions)
		}
	}
}

// TestACSVRowWithNoTimeInItIsAProblemRatherThanAnEntry.
//
// A zero-length entry is not time somebody worked. Importing it would put a
// row on a timesheet that reads as a day's work of nothing, and a row that
// says nothing is harder to notice than one that was refused.
func TestACSVRowWithNoTimeInItIsAProblemRatherThanAnEntry(t *testing.T) {
	f := newFixture(t)

	const csv = "date,hours,assignment\n" +
		"2026-03-16,1.5,Development\n" +
		"2026-03-17,0,Development\n" +
		"2026-03-18,0h00,Development\n"

	preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Valid != 1 {
		t.Errorf("%d rows valid, want the 1 with time in it", preview.Valid)
	}
	if preview.Invalid != 2 {
		t.Errorf("%d rows invalid, want the 2 with no time in them", preview.Invalid)
	}
	zeroes := 0
	for _, row := range preview.Rows {
		if strings.Contains(row.Problem, "zero") {
			zeroes++
		}
	}
	if zeroes != 2 {
		t.Errorf("%d rows were named as having no duration, want 2: %+v",
			zeroes, preview.Rows)
	}
}

// TestCSVNamesAreMatchedWithoutRegardToCase.
//
// A file exported from another system capitalises however that system does.
// Matching case-sensitively would make an import of somebody's own catalogue
// fail on every row, with "no such assignment" against names that are plainly
// there.
func TestCSVNamesAreMatchedWithoutRegardToCase(t *testing.T) {
	f := newFixture(t)

	const csv = "date,hours,customer,project,assignment\n" +
		"2026-03-16,1.5,ACME,MIGRATION,DEVELOPMENT\n" +
		"2026-03-17,2,acme,migration,development\n"

	preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Invalid != 0 {
		t.Fatalf("%d rows could not be placed, want none: %+v", preview.Invalid, preview.Rows)
	}

	imported, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 2 {
		t.Errorf("imported %d rows, want 2", imported)
	}

	// Against the catalogue that was already there, not a second copy of it
	// under a different capitalisation.
	assignments, err := f.svc.Assignments(f.ctx, 0, true)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	if len(assignments) != 1 {
		t.Errorf("%d assignments after the import, want the fixture's 1", len(assignments))
	}
}
