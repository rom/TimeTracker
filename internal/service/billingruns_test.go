package service

import (
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Recording what has been billed.
//
// The fixture's assignment is billable with the instance's default rate, so
// entries recorded against it carry an amount. The dates here sit in the week
// before the fixture's clock, which is a Monday on 2026-03-16.

// customerID is the fixture's customer, which the assignment does not carry.
func (f *fixture) customer(t *testing.T) int64 {
	t.Helper()

	project, err := f.db.GetProject(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	return project.CustomerID
}

// billed records two hours and returns the run that bills them.
func (f *fixture) billed(t *testing.T, reference string) domain.BillingRun {
	t.Helper()

	f.record(t, "2026-03-10", 2*3600)
	run, err := f.svc.RecordBillingRun(f.ctx, BillingInput{
		CustomerID: f.customer(t),
		StartsOn:   "2026-03-09", EndsOn: "2026-03-15",
		Reference: reference,
	})
	if err != nil {
		t.Fatalf("record billing run: %v", err)
	}
	return run
}

func TestBilledTimeStopsBeingOfferedForBillingAgain(t *testing.T) {
	f := newFixture(t)

	from := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	f.record(t, "2026-03-10", 2*3600)
	before, err := f.svc.BillingCandidatesFor(f.ctx, f.customer(t), from, to)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(before.Entries) != 1 || before.BillableSeconds != 2*3600 {
		t.Fatalf("%d entries and %d seconds to bill, want 1 and 7200",
			len(before.Entries), before.BillableSeconds)
	}

	run, err := f.svc.RecordBillingRun(f.ctx, BillingInput{
		CustomerID: f.customer(t),
		StartsOn:   "2026-03-09", EndsOn: "2026-03-15", Reference: "INV-1",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if run.EntryCount != 1 || run.BillableSeconds != 2*3600 {
		t.Errorf("the run says %d entries and %d seconds, want 1 and 7200",
			run.EntryCount, run.BillableSeconds)
	}

	// The whole point: the same hours cannot go out twice.
	after, err := f.svc.BillingCandidatesFor(f.ctx, f.customer(t), from, to)
	if err != nil {
		t.Fatalf("candidates again: %v", err)
	}
	if len(after.Entries) != 0 {
		t.Errorf("%d entries still offered after billing them", len(after.Entries))
	}
	if _, err := f.svc.RecordBillingRun(f.ctx, BillingInput{
		CustomerID: f.customer(t),
		StartsOn:   "2026-03-09", EndsOn: "2026-03-15", Reference: "INV-2",
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("billing the same range twice returned %v, want a refusal", err)
	}
}

func TestBilledTimeDoesNotChangeUnderTheInvoice(t *testing.T) {
	f := newFixture(t)
	run := f.billed(t, "INV-7")

	entries, err := f.svc.Entries(f.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	entry := entries[0]
	if entry.BillingRunID != run.ID {
		t.Fatalf("the entry points at run %d, want %d", entry.BillingRunID, run.ID)
	}

	// Editing, deleting and moving are all refused, and each says so.
	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: entry.AssignmentID, StartedAt: entry.StartedAt,
		DurationSeconds: 3600, Note: "Corrected",
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("editing billed time returned %v, want a refusal", err)
	}
	if err := f.svc.DeleteEntry(f.ctx, entry.ID); !errors.Is(err, ErrValidation) {
		t.Errorf("deleting billed time returned %v, want a refusal", err)
	}

	// Voiding the run is the way back, and the message says so.
	if err := f.svc.VoidBillingRun(f.ctx, run.ID); err != nil {
		t.Fatalf("void: %v", err)
	}
	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: entry.AssignmentID, StartedAt: entry.StartedAt,
		DurationSeconds: 3600, Note: "Corrected",
	}); err != nil {
		t.Errorf("editing after voiding the run: %v", err)
	}
}

func TestVoidingARunReleasesItsTimeAndKeepsTheRecord(t *testing.T) {
	f := newFixture(t)
	run := f.billed(t, "INV-9")

	if err := f.svc.VoidBillingRun(f.ctx, run.ID); err != nil {
		t.Fatalf("void: %v", err)
	}

	// The record stays, marked.
	runs, err := f.svc.BillingRuns(f.ctx, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs listed, want 1: a voided invoice is part of the history", len(runs))
	}
	if !runs[0].Voided() {
		t.Error("the run is not marked as voided")
	}

	// And the time is billable again.
	candidates, err := f.svc.BillingCandidatesFor(f.ctx, f.customer(t),
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates.Entries) != 1 {
		t.Errorf("%d entries offered after voiding, want 1", len(candidates.Entries))
	}

	// Voiding twice is a refusal rather than a second void.
	if err := f.svc.VoidBillingRun(f.ctx, run.ID); !errors.Is(err, ErrValidation) {
		t.Errorf("voiding twice returned %v, want a refusal", err)
	}
}

// Nothing unaccepted reaches an invoice: a proposal waiting on its subject and
// an hour flagged for review are both excluded, which is ASR-008 at the point
// it matters most.
func TestAProposalNobodyAcceptedIsNotBilled(t *testing.T) {
	f := newFixture(t)

	colleague, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create colleague: %v", err)
	}

	// Time recorded for somebody else is a proposal until they accept it.
	// Through the server-mode authoriser, because local mode has nobody to
	// propose to and refuses outright.
	office, ctx := officeContext(t, f)
	if _, err := office.CreateEntry(ctx, EntryInput{
		AssignmentID:    f.assignment.ID,
		StartedAt:       time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
		DurationSeconds: 3600, Note: "On their behalf",
		Billable:   true,
		OnBehalfOf: colleague.ID,
	}); err != nil {
		t.Fatalf("proxy entry: %v", err)
	}

	candidates, err := office.BillingCandidatesFor(ctx, f.customer(t),
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates.Entries) != 0 {
		t.Errorf("%d entries offered for billing, want 0: a proposal nobody "+
			"has accepted is not billable time", len(candidates.Entries))
	}
}

// Billing is the customer's administrator's. A member records time against a
// customer and may not invoice one.
func TestAMemberCannotBillAnybody(t *testing.T) {
	f := newFixture(t)
	f.record(t, "2026-03-10", 2*3600)

	stranger, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Member", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	office, _ := officeContext(t, f)
	memberCtx := auth.WithUser(f.ctx, stranger)

	if office.MayBillAnything(memberCtx) {
		t.Error("a member is told they may bill")
	}
	if _, err := office.BillingCandidatesFor(memberCtx, f.customer(t),
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Error("a member was shown what to invoice")
	}
	if _, err := office.RecordBillingRun(memberCtx, BillingInput{
		CustomerID: f.customer(t),
		StartsOn:   "2026-03-09", EndsOn: "2026-03-15",
	}); err == nil {
		t.Error("a member billed a customer")
	}
}

// Looking at an invoice after the fact: the run and the hours it covered.
//
// The screen's "what did we bill them for in March" - and the read a correction
// starts from, since the entries on a run are the ones frozen under it.
func TestARunCanBeOpenedWithTheHoursItCovered(t *testing.T) {
	f := newFixture(t)
	run := f.billed(t, "INV-2026-021")

	// The summary alone, which is what the confirmation before voiding reads.
	summary, err := f.svc.BillingRunSummary(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Reference != "INV-2026-021" || summary.CustomerName == "" {
		t.Errorf("the summary does not name the invoice or its customer: %+v", summary)
	}

	// And the run with its entries.
	opened, entries, err := f.svc.BillingRun(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.ID != run.ID {
		t.Errorf("opened run %d, want %d", opened.ID, run.ID)
	}
	if len(entries) != run.EntryCount {
		t.Errorf("the run says %d entries and produced %d; an invoice whose "+
			"detail does not match its own total is unanswerable",
			run.EntryCount, len(entries))
	}
	for _, entry := range entries {
		if entry.BillingRunID != run.ID {
			t.Errorf("entry %d belongs to run %d, not this one",
				entry.ID, entry.BillingRunID)
		}
	}

	// A run that is not there is not found, rather than an empty invoice.
	if _, _, err := f.svc.BillingRun(f.ctx, 404); err == nil {
		t.Error("a run that does not exist opened without error")
	}
	if _, err := f.svc.BillingRunSummary(f.ctx, 404); err == nil {
		t.Error("a summary of a run that does not exist was produced")
	}
}
