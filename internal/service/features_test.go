package service

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// ---------------------------------------------------------------- expenses --

// TestExpenseBillableAndReimbursableAreIndependent is the point of the whole
// design: they are separate questions and the totals must keep them apart.
func TestExpenseBillableAndReimbursableAreIndependent(t *testing.T) {
	f := newFixture(t)
	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}

	cases := []struct {
		name                       string
		billable, reimbursable     bool
		amount                     string
		markup                     int64
		wantBilled, wantReimbursed int64
	}{
		// A taxi the consultant paid for and re-charges: both.
		{"both", true, true, "100.00", 0, 10000, 10000},
		// A hotel the client booked directly: neither.
		{"neither", false, false, "500.00", 0, 0, 0},
		// A train ticket on an internal project: reimbursed, not billed.
		{"reimbursable only", false, true, "45.50", 0, 0, 4550},
		// A subcontractor invoice re-charged with a margin: billed, not
		// reimbursed, and the markup applies only to the billed side.
		{"billable with markup", true, false, "200.00", 15, 23000, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
				ProjectID: project.ID, SpentOn: f.now.Format("2006-01-02"),
				Description: tc.name, Amount: tc.amount,
				Billable: tc.billable, Reimbursable: tc.reimbursable,
				MarkupPercent: tc.markup,
			})
			if err != nil {
				t.Fatalf("create expense: %v", err)
			}
			if expense.BilledMinor != tc.wantBilled {
				t.Errorf("billed = %d, want %d", expense.BilledMinor, tc.wantBilled)
			}

			totals := f.svc.ExpenseTotals([]domain.Expense{expense})
			if got := totals.BillableByCurrency[expense.Currency]; got != tc.wantBilled {
				t.Errorf("billable total = %d, want %d", got, tc.wantBilled)
			}
			// Reimbursement is at cost: a markup is what the client pays, not
			// what the person gets back.
			if got := totals.ReimbursableByCurrency[expense.Currency]; got != tc.wantReimbursed {
				t.Errorf("reimbursable total = %d, want %d", got, tc.wantReimbursed)
			}
		})
	}
}

func TestExpenseValidation(t *testing.T) {
	f := newFixture(t)

	bad := []struct {
		name string
		in   ExpenseInput
	}{
		{"no amount", ExpenseInput{ProjectID: f.assignment.ProjectID,
			SpentOn: "2026-03-16", Amount: "0"}},
		{"no date", ExpenseInput{ProjectID: f.assignment.ProjectID, Amount: "10"}},
		{"bad date", ExpenseInput{ProjectID: f.assignment.ProjectID,
			SpentOn: "16/03/2026", Amount: "10"}},
		{"absurd markup", ExpenseInput{ProjectID: f.assignment.ProjectID,
			SpentOn: "2026-03-16", Amount: "10", Billable: true, MarkupPercent: 5000}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.CreateExpense(f.ctx, tc.in); !errors.Is(err, ErrValidation) {
				t.Errorf("got %v, want a validation error", err)
			}
		})
	}
}

// ------------------------------------------------------------ proxy entries --

// TestProxyWorkflow walks the whole consent path: propose, see it count for
// nothing, accept, see it count.
func TestProxyWorkflow(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	proposed, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
		Billable: true, OnBehalfOf: colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	if proposed.Status != domain.StatusPending {
		t.Errorf("a proxy entry was created as %q, want pending", proposed.Status)
	}
	if proposed.UserID != colleague.ID || proposed.EnteredBy != f.admin.ID {
		t.Errorf("the entry does not record both parties: user=%d entered_by=%d",
			proposed.UserID, proposed.EnteredBy)
	}
	if proposed.Counts() {
		t.Error("a pending proposal counts towards totals")
	}

	// The colleague sees it in their inbox.
	colleagueCtx := f.asUser(colleague)
	inbox, err := f.svc.Inbox(colleagueCtx)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox.Entries) != 1 {
		t.Fatalf("the inbox holds %d entries, want 1", len(inbox.Entries))
	}

	// The author cannot accept on their behalf: a consent someone else can give
	// is not consent.
	if _, err := f.svc.AcceptEntry(f.ctx, proposed.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the author accepted their own proposal: %v", err)
	}

	accepted, err := f.svc.AcceptEntry(colleagueCtx, proposed.ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Status != domain.StatusConfirmed {
		t.Errorf("after acceptance the status is %q", accepted.Status)
	}
	if !accepted.Counts() {
		t.Error("an accepted entry still does not count")
	}
	if accepted.DecidedBy != colleague.ID {
		t.Errorf("the decision was attributed to %d, want %d", accepted.DecidedBy, colleague.ID)
	}

	// A decision is final.
	if _, err := f.svc.AcceptEntry(colleagueCtx, proposed.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("a decided entry was decided again: %v", err)
	}
}

// TestProxyRejectionIsKept: the record of what was claimed must survive the
// disagreement.
func TestProxyRejectionIsKept(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	proposed, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
		Billable: true, OnBehalfOf: colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	colleagueCtx := f.asUser(colleague)
	if err := f.svc.RejectEntry(colleagueCtx, proposed.ID, "I was not there that afternoon"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// Kept, not deleted.
	entry, err := f.db.GetEntry(f.ctx, proposed.ID)
	if err != nil {
		t.Fatalf("the rejected entry was deleted: %v", err)
	}
	if entry.Status != domain.StatusRejected {
		t.Errorf("status = %q, want rejected", entry.Status)
	}
	if entry.DecisionNote == "" {
		t.Error("the reason was not recorded")
	}
	if entry.Counts() {
		t.Error("a rejected entry counts towards totals")
	}

	// And the author can see what happened to it.
	proposals, err := f.svc.ProposedByMe(f.ctx)
	if err != nil {
		t.Fatalf("proposals: %v", err)
	}
	if len(proposals) != 1 || proposals[0].Status != domain.StatusRejected {
		t.Errorf("the author cannot see the rejection: %+v", proposals)
	}
}

// TestOverlappingProposalsAreFlagged: two people being helpful about the same
// afternoon would otherwise double somebody's hours.
func TestOverlappingProposalsAreFlagged(t *testing.T) {
	f := newServerFixture(t)
	assignment, subject := f.team(t)

	for _, offset := range []time.Duration{0, 30 * time.Minute} {
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: assignment.ID, StartedAt: f.now.Add(offset),
			DurationSeconds: 3600, Billable: true, OnBehalfOf: subject.ID,
		}); err != nil {
			t.Fatalf("propose: %v", err)
		}
	}

	inbox, err := f.svc.Inbox(f.asUser(subject))
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox.Overlapping) != 2 {
		t.Errorf("overlapping proposals were not flagged: %+v", inbox.Overlapping)
	}
}

// ------------------------------------------------------------ moving time ---

// TestMoveEntriesRecalculatesBilling: the commonest correction, and the reason
// it is more than an UPDATE.
func TestMoveEntriesRecalculatesBilling(t *testing.T) {
	f := newFixture(t)

	// A second customer at a different rate and with no rounding.
	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
		Name: "Other Client", Currency: "EUR", RateMinor: 20000, // 200.00/h
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	otherProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: other.ID, Name: "Other work", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	target, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: otherProject.ID, Name: "Target", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// One hour on the fixture's customer: 1250.00/h, rounded up to the quarter.
	entry := mustCreate(t, f, f.now, 3600)
	if entry.AmountMinor != 125000 {
		t.Fatalf("precondition: amount = %d, want 125000", entry.AmountMinor)
	}

	result, err := f.svc.MoveEntries(f.ctx, []int64{entry.ID}, target.ID)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if result.Moved != 1 || result.Skipped != 0 {
		t.Errorf("moved %d, skipped %d", result.Moved, result.Skipped)
	}

	moved, err := f.svc.Entry(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if moved.AssignmentID != target.ID {
		t.Error("the entry did not move")
	}
	// Recomputed from the target: 200.00 EUR/h, not 1250.00 SEK/h. Leaving the
	// old figures would bill the new customer at the old customer's rate.
	if moved.AmountMinor != 20000 {
		t.Errorf("amount after the move = %d, want 20000", moved.AmountMinor)
	}
	if moved.Currency != "EUR" {
		t.Errorf("currency after the move = %q, want EUR", moved.Currency)
	}

	// And the move is findable in the trail, because "why did this project's
	// hours change" has to be answerable.
	events, err := f.svc.AuditTrail(f.ctx, "time_entry", entry.ID)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Action == "time_entry.move" {
			found = true
			if !strings.Contains(e.Detail, "Other Client") {
				t.Errorf("the move record does not say where it went: %s", e.Detail)
			}
		}
	}
	if !found {
		t.Error("the move was not audited")
	}
}

// TestMoveRefusesPendingEntries: moving a proposal underneath its subject would
// change what they are being asked to agree to.
func TestMoveRefusesPendingEntries(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	proposed, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now, DurationSeconds: 3600,
		Billable: true, OnBehalfOf: colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: assignment.ProjectID, Name: "Second", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	result, err := f.svc.MoveEntries(f.ctx, []int64{proposed.ID}, second.ID)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if result.Moved != 0 || result.Skipped != 1 {
		t.Errorf("a pending proposal was moved: moved=%d skipped=%d",
			result.Moved, result.Skipped)
	}
}

// ------------------------------------------------------- rate inheritance ---

// TestRateInheritsFromCustomer is the requirement stated plainly: with no rate
// on the assignment or the project, the customer's applies.
func TestRateInheritance(t *testing.T) {
	f := newFixture(t)

	// The fixture's customer carries 1250.00/h and neither the project nor the
	// assignment sets one, so the customer's rate must be used.
	entry := mustCreate(t, f, f.now, 3600)
	if entry.RateMinor != 125000 {
		t.Fatalf("the customer's rate was not inherited: %d", entry.RateMinor)
	}

	// A project rate overrides the customer's.
	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	project.RateMinor = 90000
	if err := f.svc.UpdateProject(f.ctx, project); err != nil {
		t.Fatalf("update project: %v", err)
	}
	entry = mustCreate(t, f, f.now.Add(time.Hour), 3600)
	if entry.RateMinor != 90000 {
		t.Errorf("the project rate did not override the customer's: %d", entry.RateMinor)
	}

	// An assignment rate overrides both.
	assignment, err := f.svc.Assignment(f.ctx, f.assignment.ID)
	if err != nil {
		t.Fatalf("load assignment: %v", err)
	}
	assignment.RateMinor = 75000
	if err := f.svc.UpdateAssignment(f.ctx, assignment); err != nil {
		t.Fatalf("update assignment: %v", err)
	}
	entry = mustCreate(t, f, f.now.Add(2*time.Hour), 3600)
	if entry.RateMinor != 75000 {
		t.Errorf("the assignment rate did not override: %d", entry.RateMinor)
	}
}

// ------------------------------------------------------------ CSV import ----

func TestCSVImportPreviewAndCommit(t *testing.T) {
	f := newFixture(t)

	csv := "date,hours,customer,project,assignment,note,billable\n" +
		"2026-03-16,1.5,Acme,Migration,Development,Refactoring,yes\n" +
		"2026-03-17,2h30,Acme,Migration,Development,More work,yes\n" +
		"2026-03-18,45m,Acme,Migration,Development,Meeting,no\n"

	preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Fatal != "" {
		t.Fatalf("preview reported a fatal problem: %s", preview.Fatal)
	}
	if preview.Valid != 3 || preview.Invalid != 0 {
		t.Fatalf("preview: %d valid, %d invalid; rows: %+v",
			preview.Valid, preview.Invalid, preview.Rows)
	}
	// 1.5h + 2.5h + 45m
	if want := int64(5400 + 9000 + 2700); preview.TotalSeconds != want {
		t.Errorf("preview total = %d, want %d", preview.TotalSeconds, want)
	}

	// Nothing was written by the preview.
	before, _ := f.svc.Entries(f.ctx, EntryFilter{})
	if len(before) != 0 {
		t.Fatalf("the preview created %d entries; it must write nothing", len(before))
	}

	imported, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 3 {
		t.Errorf("imported %d rows, want 3", imported)
	}

	after, _ := f.svc.Entries(f.ctx, EntryFilter{
		From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	})
	if len(after) != 3 {
		t.Errorf("after import there are %d entries, want 3", len(after))
	}
}

// TestCSVImportIsAllOrNothing: a partly succeeded import leaves the user
// reconciling two sources, which is worse than a clear failure.
func TestCSVImportIsAllOrNothing(t *testing.T) {
	f := newFixture(t)

	csv := "date,hours,assignment\n" +
		"2026-03-16,1.5,Development\n" +
		"2026-03-17,2.0,No Such Assignment\n"

	preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Valid != 1 || preview.Invalid != 1 {
		t.Fatalf("preview: %d valid, %d invalid", preview.Valid, preview.Invalid)
	}
	// The problem must name the row, so the user can find it in their file.
	if preview.Rows[1].Line != 3 {
		t.Errorf("the problem row is reported as line %d, want 3", preview.Rows[1].Line)
	}

	if _, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(csv), false); err == nil {
		t.Fatal("an import with an unmatched row succeeded")
	}

	entries, _ := f.svc.Entries(f.ctx, EntryFilter{})
	if len(entries) != 0 {
		t.Errorf("a failed import wrote %d entries; it must write none", len(entries))
	}
}

// TestCSVImportRejectsAmbiguousDates: guessing between 3 April and 4 March would
// silently put a month of work on the wrong days.
func TestCSVImportRejectsAmbiguousDates(t *testing.T) {
	f := newFixture(t)

	csv := "date,hours,assignment\n03/04/2026,1.5,Development\n"
	preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(csv), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Invalid != 1 {
		t.Error("an ambiguous date was accepted rather than reported")
	}
	if !strings.Contains(preview.Rows[0].Problem, "date") {
		t.Errorf("the problem does not mention the date: %q", preview.Rows[0].Problem)
	}
}

// TestCSVImportCreatesMissingCatalogue when asked to.
func TestCSVImportCreatesMissingCatalogue(t *testing.T) {
	f := newFixture(t)

	csv := "date,hours,customer,project,assignment\n" +
		"2026-03-16,1.5,New Client,New Project,New Task\n"

	preview, err := f.svc.ParseTimeCSV(f.ctx, strings.NewReader(csv), true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Valid != 1 {
		t.Fatalf("preview rejected the row: %+v", preview.Rows)
	}
	if len(preview.Rows[0].WillCreate) == 0 {
		t.Error("the preview does not say what it would create")
	}

	if _, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(csv), true); err != nil {
		t.Fatalf("import: %v", err)
	}

	customers, _ := f.svc.Customers(f.ctx, false)
	found := false
	for _, c := range customers {
		if c.Name == "New Client" {
			found = true
		}
	}
	if !found {
		t.Error("the customer was not created")
	}
}

// TestCSVImportHandlesExcelBOM: Excel writes one, and it would otherwise make
// the first column name unmatchable.
func TestCSVImportHandlesExcelBOM(t *testing.T) {
	f := newFixture(t)

	// \ufeff is the UTF-8 byte order mark Excel writes.
	csv := "\ufeffdate,hours,assignment\n2026-03-16,1.5,Development\n"
	preview, err := f.svc.ParseTimeCSV(f.ctx, bytes.NewReader([]byte(csv)), false)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Fatal != "" {
		t.Fatalf("a file with a BOM was rejected: %s", preview.Fatal)
	}
	if preview.Valid != 1 {
		t.Errorf("a file with a BOM did not parse: %+v", preview.Rows)
	}
}

// ------------------------------------------------------------- favourites ---

func TestSetFavourite(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.SetFavourite(f.ctx, f.assignment.ID, true); err != nil {
		t.Fatalf("set favourite: %v", err)
	}
	assignment, err := f.svc.Assignment(f.ctx, f.assignment.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !assignment.Favourite {
		t.Error("the assignment was not marked as a favourite")
	}

	// Favourites sort first, which is the point of marking one.
	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "AAA Sorts First Alphabetically",
		BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	assignments, err := f.svc.Assignments(f.ctx, 0, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if assignments[0].ID != f.assignment.ID {
		t.Errorf("the favourite does not sort first; got %q", assignments[0].Name)
	}
	_ = second

	if err := f.svc.SetFavourite(f.ctx, f.assignment.ID, false); err != nil {
		t.Fatalf("unset favourite: %v", err)
	}
	assignment, _ = f.svc.Assignment(f.ctx, f.assignment.ID)
	if assignment.Favourite {
		t.Error("the favourite was not removed")
	}
}

// --------------------------------------------------------------- settings ---

func TestSettingsValidation(t *testing.T) {
	f := newFixture(t)

	// An unknown rounding rule would silently degrade to "no rounding" when read
	// back, which is a quiet way to under-bill.
	err := f.svc.UpdateSettings(f.ctx, SettingsInput{DefaultRounding: "up/999/0"})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("an unknown rounding rule was accepted: %v", err)
	}

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "EUR", DefaultRounding: "none/0/7200",
		WeekStart: 1, MaxTimerHours: 10,
		ShowClock: true, ShowTimeAndDate: false,
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	settings, err := f.svc.Settings(f.ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if settings.DefaultRounding != "none/0/7200" {
		t.Errorf("rounding = %q", settings.DefaultRounding)
	}
	if !settings.ShowClock || settings.ShowTimeAndDate {
		t.Errorf("display toggles were not saved: clock=%v datetime=%v",
			settings.ShowClock, settings.ShowTimeAndDate)
	}
	if settings.MaxTimerSeconds != 36000 {
		t.Errorf("max timer = %d, want 36000", settings.MaxTimerSeconds)
	}
}
