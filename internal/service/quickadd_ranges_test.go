package service

import (
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/domain"
)

// The two-second path: one line of text becomes an entry.
//
// Anything ambiguous has to come back as a question rather than a guess. A
// wrong guess that silently becomes billable time is worse than a second
// attempt, because nobody checks a line they have already typed.

// TestQuickAddReadsATimeRange.
//
// "09:00-10:30 Development" and "1h30 Development" are the same statement made
// two ways, and both are natural. A range also fixes *when* the work was, which
// a bare duration does not: a duration means "this much, ending now".
func TestQuickAddReadsATimeRange(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)

	cases := []struct {
		name          string
		input         string
		wantStartHour int
		wantSeconds   int64
	}{
		{"a colon range", "09:00-10:30 Development", 9, 90 * 60},
		{"a four-digit range", "0900-1030 Development", 9, 90 * 60},
		{"single-digit hours", "9:00-10:00 Development", 9, 60 * 60},
		{"a range that crosses midnight", "23:00-01:30 Development", 23, 150 * 60},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := f.svc.ParseQuickAdd(f.ctx, tc.input)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if result.Ambiguous {
				t.Fatalf("%q came back as a question: %s", tc.input, result.Reason)
			}
			if result.Entry.EndedAt == nil {
				t.Fatal("a time range produced no end time")
			}
			if got := result.Entry.StartedAt.Hour(); got != tc.wantStartHour {
				t.Errorf("starts at %02d:00, want %02d:00", got, tc.wantStartHour)
			}
			if got := int64(result.Entry.EndedAt.Sub(result.Entry.StartedAt).Seconds()); got != tc.wantSeconds {
				t.Errorf("the range spans %ds, want %ds", got, tc.wantSeconds)
			}
			if result.Assignment.ID != f.assignment.ID {
				t.Errorf("matched assignment %d, want %d",
					result.Assignment.ID, f.assignment.ID)
			}
		})
	}

	// And the range is actually recorded, rather than only parsed.
	entry, _, err := f.svc.QuickAdd(f.ctx, "09:00-10:30 Development stand-up and review")
	if err != nil {
		t.Fatalf("quick add: %v", err)
	}
	if entry.DurationSeconds != 90*60 {
		t.Errorf("recorded %ds, want 90 minutes", entry.DurationSeconds)
	}
	if entry.Note != "stand-up and review" {
		t.Errorf("the note is %q, want what was left after the range and the "+
			"assignment", entry.Note)
	}
}

// TestQuickAddRefusesWhatItCannotReadAsATime.
//
// Each of these looks enough like a range to be worth refusing clearly. A
// hyphen in a note must not be read as a time range, and a range with a
// nonsense clock in it has to fall through to "no duration found" rather than
// producing an entry at an hour nobody typed.
func TestQuickAddRefusesWhatItCannotReadAsATime(t *testing.T) {
	f := newFixture(t)

	for _, input := range []string{
		"Development re-indexing",
		"half-nine-to-ten Development",
		"9:aa-10:00 Development",
		"09:00-teatime Development",
		"900-1000 Development",
		"-10:00 Development",
	} {
		t.Run(input, func(t *testing.T) {
			result, err := f.svc.ParseQuickAdd(f.ctx, input)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !result.Ambiguous {
				t.Errorf("%q was read as an entry starting %s for %ds, rather than "+
					"coming back as a question", input,
					result.Entry.StartedAt, result.Entry.DurationSeconds)
			}
			if result.Reason == "" {
				t.Error("the question has no reason attached, so a screen cannot say what to fix")
			}
		})
	}
}

// TestQuickAddAsksWhenSeveralAssignmentsMatch.
//
// Two assignments whose names both contain the word typed. Picking one would
// be a guess at which engagement the time belongs to, which is the one guess
// that ends up on an invoice.
func TestQuickAddAsksWhenSeveralAssignmentsMatch(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if _, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Development support", BillableDefault: true,
	}); err != nil {
		t.Fatalf("create a second matching assignment: %v", err)
	}

	result, err := f.svc.ParseQuickAdd(f.ctx, "2h Development")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !result.Ambiguous {
		t.Fatalf("a word matching two assignments was resolved to %q",
			result.Assignment.Name)
	}
	if !strings.Contains(result.Reason, "match") {
		t.Errorf("the reason is %q, which does not say that several matched", result.Reason)
	}
	if len(result.Candidates) < 2 {
		t.Errorf("%d candidates offered, want both so the screen can ask",
			len(result.Candidates))
	}

	// Recording it is refused rather than resolved.
	if _, parsed, err := f.svc.QuickAdd(f.ctx, "2h Development"); err == nil && !parsed.Ambiguous {
		t.Error("an ambiguous line was recorded anyway")
	}

	// Naming it precisely resolves it.
	exact, err := f.svc.ParseQuickAdd(f.ctx, "2h Development support")
	if err != nil {
		t.Fatalf("parse the exact name: %v", err)
	}
	if exact.Ambiguous {
		t.Errorf("an exact name was still ambiguous: %s", exact.Reason)
	}
}

// TestQuickAddOnAFreshInstallation.
//
// There is no history to match against before the first entry, so the match
// falls back to the whole catalogue. Without that fallback the feature would
// be unusable until somebody had already used it, which is the kind of
// circular failure that looks like the application being broken.
func TestQuickAddOnAFreshInstallation(t *testing.T) {
	f := newFixture(t)

	recent, err := f.svc.RecentAssignments(f.ctx, 50)
	if err != nil {
		t.Fatalf("read recent assignments: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("%d recent assignments before anything was recorded, so the "+
			"fallback below is not what is being tested", len(recent))
	}

	result, err := f.svc.ParseQuickAdd(f.ctx, "2h Development")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Ambiguous {
		t.Fatalf("nothing matched on a fresh installation: %s", result.Reason)
	}
	if result.Assignment.ID != f.assignment.ID {
		t.Errorf("matched %d, want the catalogue's only assignment %d",
			result.Assignment.ID, f.assignment.ID)
	}
}

// TestABareDurationMeansWorkEndingNow.
//
// How people log time they have just finished. The start is computed
// backwards, so "2h" at four o'clock is two until four rather than two
// starting now - which would put the work in the future.
func TestABareDurationMeansWorkEndingNow(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)

	result, err := f.svc.ParseQuickAdd(f.ctx, "2h Development")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Entry.StartedAt.Hour() != 14 {
		t.Errorf("two hours ending at four starts at %02d:00, want 14:00",
			result.Entry.StartedAt.Hour())
	}
	if result.Entry.DurationSeconds != 2*3600 {
		t.Errorf("recorded %ds, want two hours", result.Entry.DurationSeconds)
	}
	if result.Entry.EndedAt != nil {
		t.Errorf("a bare duration set an explicit end of %s; the length is the "+
			"statement, and the end follows from it", result.Entry.EndedAt)
	}

	// Tags come out of anywhere in the line, and are not left in the note.
	tagged, err := f.svc.ParseQuickAdd(f.ctx, "2h Development #incident fixing the index #urgent")
	if err != nil {
		t.Fatalf("parse a tagged line: %v", err)
	}
	if len(tagged.Tags) != 2 {
		t.Errorf("found %v, want both tags", tagged.Tags)
	}
	if strings.Contains(tagged.Entry.Note, "#") {
		t.Errorf("the note still carries a tag: %q", tagged.Entry.Note)
	}
	if tagged.Entry.Note != "fixing the index" {
		t.Errorf("the note is %q, want what was left after the tags", tagged.Entry.Note)
	}
}

// TestMovingTimeReportsWhatItCouldNotMove.
//
// Moving time recorded against the wrong assignment is a bulk correction over
// a list somebody ticked. A single refusal must not abandon the rest, and the
// count of what was skipped is what makes a partial result visible rather than
// silently smaller than expected.
func TestMovingTimeReportsWhatItCouldNotMove(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	target, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create the target assignment: %v", err)
	}

	movable := mustCreate(t, f, at(f.now, 9, 0), 3600)

	// An id that is not an entry at all, alongside one that is: a stale tick
	// from a list somebody left open.
	result, err := f.svc.MoveEntries(f.ctx, []int64{movable.ID, 9999}, target.ID)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if result.Moved != 1 {
		t.Errorf("moved %d entries, want the 1 that exists", result.Moved)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped %d, want the 1 that does not exist - a partial result "+
			"has to be visible", result.Skipped)
	}
	if result.Target.ID != target.ID {
		t.Errorf("the result names assignment %d as the target, want %d",
			result.Target.ID, target.ID)
	}

	moved, err := f.svc.Entry(f.ctx, movable.ID)
	if err != nil {
		t.Fatalf("read the moved entry: %v", err)
	}
	if moved.AssignmentID != target.ID {
		t.Errorf("the entry is still on assignment %d", moved.AssignmentID)
	}

	// A submitted week is closed, so time in it cannot be moved either - and
	// that is a skip rather than a failure of the whole correction.
	locked := mustCreate(t, f, at(f.now.AddDate(0, 0, 1), 9, 0), 3600)
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit the week: %v", err)
	}
	after, err := f.svc.MoveEntries(f.ctx, []int64{locked.ID}, f.assignment.ID)
	if err != nil {
		t.Fatalf("move inside a submitted week: %v", err)
	}
	if after.Moved != 0 || after.Skipped != 1 {
		t.Errorf("moving time in a submitted week moved %d and skipped %d, "+
			"want 0 and 1", after.Moved, after.Skipped)
	}

	// Moving nothing is refused by name rather than reported as a successful
	// move of nothing, which would read as having worked.
	if _, err := f.svc.MoveEntries(f.ctx, nil, target.ID); err == nil {
		t.Error("moving an empty selection reported success")
	} else if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("the refusal reads %q, which does not say what to select", err)
	}
}

// TestMovingCostsReportsWhatItCouldNotMove.
//
// The same correction for expenses, which hang off a project rather than an
// assignment.
func TestMovingCostsReportsWhatItCouldNotMove(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	target, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: project.CustomerID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create the target project: %v", err)
	}

	expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi", Amount: "100", Billable: true,
	})
	if err != nil {
		t.Fatalf("record an expense: %v", err)
	}

	result, err := f.svc.MoveExpenses(f.ctx, []int64{expense.ID, 9999}, target.ID)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if result.Moved != 1 {
		t.Errorf("moved %d expenses, want the 1 that exists", result.Moved)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped %d, want the 1 that does not exist", result.Skipped)
	}

	moved, err := f.svc.Expense(f.ctx, expense.ID)
	if err != nil {
		t.Fatalf("read the moved expense: %v", err)
	}
	if moved.ProjectID != target.ID {
		t.Errorf("the expense is still on project %d", moved.ProjectID)
	}
}

// TestTheCurrencyTermsAreTypedIn.
//
// Amounts on the terms screen are typed in the customer's currency, so "2.50"
// means 2.50 of what that account is invoiced in. A project's terms inherit
// the currency from the customer above them, and an instance with no customer
// in play falls back to the configured default - without which the screen
// would have to refuse to render.
func TestTheCurrencyTermsAreTypedIn(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	customerCurrency, err := f.svc.TermsCurrency(f.ctx, domain.TermsForCustomer, project.CustomerID)
	if err != nil {
		t.Fatalf("customer currency: %v", err)
	}
	if customerCurrency != "SEK" {
		t.Errorf("the customer's terms are in %q, want SEK", customerCurrency)
	}

	projectCurrency, err := f.svc.TermsCurrency(f.ctx, domain.TermsForProject, project.ID)
	if err != nil {
		t.Fatalf("project currency: %v", err)
	}
	if projectCurrency != customerCurrency {
		t.Errorf("the project's terms are in %q and its customer's in %q; a "+
			"project is invoiced in its customer's currency",
			projectCurrency, customerCurrency)
	}

	// A scope that names nothing falls back to the instance default rather
	// than to an empty string, which would render as a bare number.
	fallback, err := f.svc.TermsCurrency(f.ctx, domain.TermsForProject, 9999)
	if err == nil && fallback == "" {
		t.Error("terms for a project that does not exist came back with no currency")
	}

	// Terms in force on a day, resolved through the customer above the project.
	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2026-01-01",
		Rules:         domain.RateRules{OvertimeMultiplierPct: 150},
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}
	rules, err := f.svc.TermsOn(f.ctx, project.CustomerID, project.ID, f.now)
	if err != nil {
		t.Fatalf("resolve terms: %v", err)
	}
	if rules.OvertimeMultiplierPct != 150 {
		t.Errorf("the overtime multiplier in force is %d%%, want 150%%",
			rules.OvertimeMultiplierPct)
	}
}

// TestAPreviewOfSomethingThatCannotBePreviewed.
//
// The preview is a convenience: a receipt shown inline instead of downloaded.
// A file it cannot render has to be refused as such rather than crash the
// screen the attachment is listed on.
func TestAPreviewOfSomethingThatCannotBePreviewed(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	entry := mustCreate(t, f, f.now, 3600)

	// An upload whose name and contents disagree is refused before it is
	// stored, so there is nothing to preview: the type is sniffed from the
	// bytes rather than taken from the client's claim.
	if _, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry, entry.ID,
		"receipt.png", strings.NewReader("this is not a PNG")); err == nil {
		t.Error("a text file named .png was stored as an image")
	}

	// Something storable that no preview knows how to render.
	attachment, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry, entry.ID,
		"archive.zip", strings.NewReader("PK\x03\x04 not really a zip either"))
	if err != nil {
		t.Skipf("nothing unpreviewable could be stored to test with: %v", err)
	}

	_, result, err := f.svc.PreviewAttachment(f.ctx, attachment.ID)
	if err != nil {
		// A refusal is the right answer for something unrenderable.
		return
	}
	// If it did not refuse, it must at least not claim to have rendered
	// something it could not read.
	if result.Kind == "" {
		t.Error("the preview reported neither a kind nor an error")
	}

}
