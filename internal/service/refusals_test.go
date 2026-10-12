package service

import (
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The refusals: what each operation says no to, and in what words.
//
// Every one of these is a case somebody reaches by using the application
// normally - an old tab, a week already submitted, a question already
// answered, a file exported from another system. The refusal is the feature:
// silently doing something slightly different is what produces a timesheet
// nobody can reconcile.

// TestATimerCannotBeStartedOnSomethingArchived.
//
// A quick-start button, or a bookmarked form, outliving the assignment it
// points at. Recording against something taken off every form would be time
// nobody sees until an invoice.
func TestATimerCannotBeStartedOnSomethingArchived(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.SetAssignmentArchived(f.ctx, f.assignment.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err == nil {
		t.Fatal("a timer was started on an archived assignment")
	}
	if !strings.Contains(err.Error(), "archiv") {
		t.Errorf("the refusal reads %q, which does not say why", err)
	}
	if !strings.Contains(err.Error(), f.assignment.Name) {
		t.Errorf("the refusal reads %q, which does not name what was archived", err)
	}

	// Restoring it makes the button work again, so archiving is not a
	// one-way door.
	if err := f.svc.SetAssignmentArchived(f.ctx, f.assignment.ID, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, ""); err != nil {
		t.Errorf("a timer on a restored assignment = %v", err)
	}
}

// TestStoppingATimerTwiceIsNotADoubledDuration.
//
// Two tabs, or a double click. The second stop finds the entry already
// stopped, which is success rather than an error - and must not add the time
// again.
func TestStoppingATimerTwiceIsNotADoubledDuration(t *testing.T) {
	f := newFixture(t)

	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	f.now = f.now.Add(time.Hour)
	first, err := f.svc.StopTimer(f.ctx, started.ID)
	if err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if first.DurationSeconds != 3600 {
		t.Fatalf("the first stop recorded %ds, want an hour", first.DurationSeconds)
	}

	f.now = f.now.Add(time.Hour)
	second, err := f.svc.StopTimer(f.ctx, started.ID)
	if err != nil {
		t.Fatalf("the second stop was an error rather than a no-op: %v", err)
	}
	if second.DurationSeconds != 3600 {
		t.Errorf("the second stop made the entry %ds long, want the hour it ran for",
			second.DurationSeconds)
	}
}

// TestATimerStoppedBeforeItStartedIsNotANegativeDuration.
//
// The clock moving backwards, or an entry hand-edited to start later than now.
// A negative duration would subtract from the week's total, poisoning every
// figure above it, so it is clamped to zero.
//
// It is *not* marked for review, although StopTimer's comment on the clamp
// says "clamp and flag it for a human instead": the only flag that write sets
// is the long-timer one. So an entry that could not be timed records zero and
// nothing draws anybody's attention to it. Pinned as what the application
// does, with the discrepancy named, rather than asserted as what the comment
// promises.
func TestATimerStoppedBeforeItStartedIsNotANegativeDuration(t *testing.T) {
	f := newFixture(t)

	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Time runs backwards, which is what a corrected system clock looks like.
	f.now = f.now.Add(-2 * time.Hour)
	stopped, err := f.svc.StopTimer(f.ctx, started.ID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.DurationSeconds != 0 {
		t.Errorf("the entry is %ds long, want it clamped to zero - a negative "+
			"duration would subtract from the week's total",
			stopped.DurationSeconds)
	}
	if stopped.Flagged {
		t.Log("the clamped entry is now flagged for review, which is what the " +
			"comment on the clamp describes; this test's note is out of date")
	}

	// A timer left running past the configured ceiling *is* flagged, which is
	// the flag that write does set - included here so the difference between
	// the two cases is visible in one place.
	settings, err := f.svc.Settings(f.ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.MaxTimerSeconds <= 0 {
		t.Skip("no timer ceiling is configured, so there is nothing to compare against")
	}
	overnight, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "left running")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	f.now = f.now.Add(time.Duration(settings.MaxTimerSeconds+3600) * time.Second)
	long, err := f.svc.StopTimer(f.ctx, overnight.ID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !long.Flagged {
		t.Error("a timer left running past the ceiling was not marked for review, " +
			"so it would be billed silently")
	}
}

// TestASubmittedWeekIsClosedToChanges.
//
// Submitting is a statement about specific figures. If the week kept moving
// afterwards, an approval would be a decision about something that no longer
// exists - so recording, editing and deleting inside it are all refused until
// it is withdrawn or sent back.
func TestASubmittedWeekIsClosedToChanges(t *testing.T) {
	f := newFixture(t)

	existing := mustCreate(t, f, at(f.now, 9, 0), 3600)
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Submitting again is refused by name rather than resubmitting silently.
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err == nil {
		t.Error("a submitted week was submitted again")
	}

	end := at(f.now, 15, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 14, 0), EndedAt: &end,
	}); err == nil {
		t.Error("time was recorded inside a submitted week")
	}
	if _, err := f.svc.UpdateEntry(f.ctx, existing.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: existing.StartedAt, DurationSeconds: 7200,
	}); err == nil {
		t.Error("an entry inside a submitted week was edited")
	}
	if err := f.svc.DeleteEntry(f.ctx, existing.ID); err == nil {
		t.Error("an entry inside a submitted week was deleted")
	}

	// Withdrawing reopens it.
	if err := f.svc.WithdrawWeek(f.ctx, f.now); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := f.svc.UpdateEntry(f.ctx, existing.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: existing.StartedAt, DurationSeconds: 7200,
	}); err != nil {
		t.Errorf("a withdrawn week is still closed: %v", err)
	}

	// Withdrawing a week nobody submitted is refused rather than silently
	// doing nothing.
	if err := f.svc.WithdrawWeek(f.ctx, f.now.AddDate(0, 0, 14)); err == nil {
		t.Error("a week that was never submitted was withdrawn")
	}
}

// TestAWeekCanOnlyBeDecidedFromTheStateItIsIn.
//
// Approving something nobody submitted, or reopening something nobody
// approved, is a stale button in a queue somebody left open. Each refusal
// names the state the week is actually in, because that is the only way the
// person pressing it finds out what happened.
func TestAWeekCanOnlyBeDecidedFromTheStateItIsIn(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	theirs := f.asUser(colleague)
	end := time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC)
	if _, err := f.svc.CreateEntry(theirs, EntryInput{
		AssignmentID: assignment.ID,
		StartedAt:    time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end, Billable: true,
	}); err != nil {
		t.Fatalf("record their time: %v", err)
	}

	const week = "2026-03-16"

	// Nothing has been submitted yet.
	if err := f.svc.ApproveWeek(f.ctx, colleague.ID, week); err == nil {
		t.Error("a week nobody submitted was approved")
	}
	if err := f.svc.RejectWeek(f.ctx, colleague.ID, week, "no"); err == nil {
		t.Error("a week nobody submitted was sent back")
	}
	if err := f.svc.ReopenWeek(f.ctx, colleague.ID, week, "no"); err == nil {
		t.Error("a week nobody approved was reopened")
	}

	if _, err := f.svc.SubmitWeek(theirs, time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Reopening is for an approved week, not a submitted one.
	err := f.svc.ReopenWeek(f.ctx, colleague.ID, week, "no")
	if err == nil {
		t.Error("a submitted week was reopened rather than approved or sent back")
	} else if !strings.Contains(err.Error(), "approved") {
		t.Errorf("the refusal reads %q, which does not say what state is needed", err)
	}

	if err := f.svc.ApproveWeek(f.ctx, colleague.ID, week); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// And approving twice is refused, naming the state it is in now.
	if err := f.svc.ApproveWeek(f.ctx, colleague.ID, week); err == nil {
		t.Error("an approved week was approved again")
	}
	if err := f.svc.ReopenWeek(f.ctx, colleague.ID, week, "it changed"); err != nil {
		t.Errorf("reopening an approved week = %v", err)
	}

	// A reason long enough to be a paste of the whole timesheet is refused:
	// it would be stored, shown, and exported.
	if _, err := f.svc.SubmitWeek(theirs, time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if err := f.svc.RejectWeek(f.ctx, colleague.ID, week, strings.Repeat("x", 2000)); err == nil {
		t.Error("a reason of two thousand characters was accepted")
	}
}

// TestAnIdleQuestionIsAskedOnceAndAnsweredOnce.
//
// The page reports what it saw; a person decides what it meant. An
// overlapping report widens the question already open rather than asking a
// second one, and a question already answered cannot be answered again - which
// is what stops a second tab trimming the entry twice.
func TestAnIdleQuestionIsAskedOnceAndAnsweredOnce(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	if recorded, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 11, 0), at(f.now, 12, 0), domain.IdleAsleep); err != nil || !recorded {
		t.Fatalf("record idle: %v, %v", recorded, err)
	}

	// An overlapping stretch widens the open question.
	if recorded, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 11, 30), at(f.now, 13, 0), domain.IdleAsleep); err != nil || !recorded {
		t.Fatalf("record an overlapping stretch: %v, %v", recorded, err)
	}
	pending, err := f.svc.PendingIdle(f.ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d questions open, want 1 - an overlapping report widens the "+
			"one already asked rather than asking again", len(pending))
	}
	if got := pending[0].Observation.EndedAt; !got.Equal(at(f.now, 13, 0)) {
		t.Errorf("the question now ends at %s, want it widened to 13:00", got)
	}

	// An unknown source is refused: it is stored on the row and shown to
	// somebody, so it cannot be whatever a caller sends.
	if _, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 14, 0), at(f.now, 14, 30), domain.IdleSource("daydreaming")); err == nil {
		t.Error("an idle observation was recorded with a source nothing recognises")
	}

	// So is an unknown decision.
	id := pending[0].Observation.ID
	if err := f.svc.ResolveIdle(f.ctx, id, domain.IdleResolution("maybe")); err == nil {
		t.Error("an idle question was answered with a resolution nothing recognises")
	}

	if err := f.svc.ResolveIdle(f.ctx, id, domain.IdleKeep); err != nil {
		t.Fatalf("answer the question: %v", err)
	}

	// Twice is refused by name, rather than trimming the entry a second time.
	err = f.svc.ResolveIdle(f.ctx, id, domain.IdleDiscard)
	if err == nil {
		t.Fatal("an answered question was answered again")
	}
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("the refusal reads %q, which does not say it was already decided", err)
	}
}

// TestACSVWithoutTheColumnsItNeeds.
//
// The header is the only thing the importer has to go on. Each of these is a
// file somebody will try, and each refusal has to name what it was looking
// for - "no date column found" with the spellings it accepts is actionable;
// "could not import" is not.
func TestACSVWithoutTheColumnsItNeeds(t *testing.T) {
	f := newFixture(t)

	cases := map[string]struct {
		csv  string
		says string
	}{
		"no date column": {
			"hours,assignment\n1.5,Development\n", "date",
		},
		"no duration column": {
			"date,assignment\n2026-03-16,Development\n", "duration",
		},
		"nothing to record against": {
			"date,hours\n2026-03-16,1.5\n", "assignment",
		},
		"a header and nothing else": {
			"date,hours,assignment\n", "header row",
		},
		"not a CSV at all": {
			"", "header row",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(tc.csv), false)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if preview.Fatal == "" {
				t.Fatalf("a file with %s was accepted", name)
			}
			if !strings.Contains(preview.Fatal, tc.says) {
				t.Errorf("the refusal reads %q, which does not mention %q",
					preview.Fatal, tc.says)
			}
		})
	}

	// And the commit path refuses the same files, rather than importing part
	// of one the preview called fatal.
	if _, err := f.svc.ImportTimeCSV(f.ctx,
		strings.NewReader("hours,assignment\n1.5,Development\n"), false); err == nil {
		t.Error("a file with no date column was imported")
	}
}

// TestAnAmbiguousCSVRowIsAProblemRatherThanAGuess.
//
// Two assignments with the same name under different customers, which is
// ordinary: every engagement has "Development". Picking one would put hours
// on the wrong customer's invoice, so the row is named and the file is not
// importable until it says which.
func TestAnAmbiguousCSVRowIsAProblemRatherThanAGuess(t *testing.T) {
	f := newFixture(t)

	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Globex", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create a second customer: %v", err)
	}
	project, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: other.ID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create a second project: %v", err)
	}
	if _, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Development", BillableDefault: true,
	}); err != nil {
		t.Fatalf("create a clashing assignment: %v", err)
	}

	preview, err := f.svc.ParseTimeCSV(f.ctx,
		strings.NewReader("date,hours,assignment\n2026-03-16,1.5,Development\n"), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Invalid != 1 {
		t.Fatalf("%d rows invalid, want the 1 that is ambiguous: %+v",
			preview.Invalid, preview.Rows)
	}
	if !strings.Contains(preview.Rows[0].Problem, "matches") {
		t.Errorf("the problem reads %q, which does not say that several matched",
			preview.Rows[0].Problem)
	}

	// Naming the customer resolves it.
	resolved, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(
		"date,hours,customer,assignment\n2026-03-16,1.5,Globex,Development\n"), false)
	if err != nil {
		t.Fatalf("preview with a customer named: %v", err)
	}
	if resolved.Invalid != 0 {
		t.Errorf("naming the customer left %d rows ambiguous: %+v",
			resolved.Invalid, resolved.Rows)
	}
}

// TestImportingIntoACatalogueThatDoesNotExistYet.
//
// Importing a year of history from another system, where the file's own
// columns are all there is to build a catalogue from. A row naming no customer
// still has to land somewhere, so it goes under a name somebody can find and
// rename rather than under an empty one.
func TestImportingIntoACatalogueThatDoesNotExistYet(t *testing.T) {
	f := newFixture(t)

	// Only a duration and a date: nothing to place it under at all.
	imported, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(
		"date,hours,project\n2026-03-16,1.5,Website\n"), true)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported %d rows, want 1", imported)
	}

	customers, err := f.svc.Customers(f.ctx, true)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	var names []string
	for _, customer := range customers {
		names = append(names, customer.Name)
	}
	found := false
	for _, name := range names {
		if name == "Imported" {
			found = true
		}
	}
	if !found {
		t.Errorf("the created catalogue is %v; a row naming no customer must land "+
			"under a name somebody can find and rename", names)
	}

	// An assignment is created from the project's name when the file has no
	// assignment column, rather than from nothing.
	assignments, err := f.svc.Assignments(f.ctx, 0, true)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	named := false
	for _, assignment := range assignments {
		if assignment.Name == "Website" {
			named = true
		}
	}
	if !named {
		t.Error("no assignment was created under the project's own name")
	}
}
