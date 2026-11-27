package service

import (
	"bytes"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// What a backup carries.
//
// It carried the catalogue, the entries, the expenses and the attachments, and
// declared itself complete. Six tables were not in it, and the worst
// consequence was not losing data: it was that restored time which had been
// invoiced came back unbilled, offered itself for billing again, and became
// editable under a customer's invoice. The feature that exists to stop the
// same fortnight going out twice was undone by the recovery path.
//
// These tests are about what survives the round trip, one table at a time,
// because that is how the omissions happened: each was added to the schema
// without being added here.

// fullInstance gives a fixture with one of everything worth backing up.
func fullInstance(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)

	// Time, tagged, so the tags have something to hang on.
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 7200, Note: "Billable work",
		BillableFromAssignment: true,
		Tags:                   []string{"incident", "migration"},
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_ = entry

	// Dated terms, which decide what work was worth.
	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: f.customer(t),
		EffectiveFrom: "2026-01-01", Note: "Renewal",
		Rules: domain.RateRules{OvertimeMultiplierPct: 150, ExpenseMarkupPct: 10},
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}

	// Leave, both kinds.
	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind: domain.AbsenceHoliday, StartsOn: "2026-03-23", EndsOn: "2026-03-27",
		Note: "Spring week",
	}); err != nil {
		t.Fatalf("record absence: %v", err)
	}
	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind: domain.AbsencePublic, StartsOn: "2026-12-25", EndsOn: "2026-12-25",
		Note: "Christmas Day",
	}); err != nil {
		t.Fatalf("record public holiday: %v", err)
	}

	// A weekday template.
	if err := f.svc.SaveRoutine(f.ctx, domain.Routine{
		AssignmentID: f.assignment.ID, Name: "Standup",
		Weekdays: []int{1, 2, 3, 4, 5}, DurationSeconds: 900, Active: true,
	}); err != nil {
		t.Fatalf("save routine: %v", err)
	}

	// And the invoice that went out.
	if _, err := f.svc.RecordBillingRun(f.ctx, BillingInput{
		CustomerID: f.customer(t),
		StartsOn:   f.now.AddDate(0, 0, -3).Format("2006-01-02"),
		EndsOn:     f.now.AddDate(0, 0, 3).Format("2006-01-02"),
		Reference:  "INV-2026-014",
	}); err != nil {
		t.Fatalf("record billing run: %v", err)
	}

	return f
}

// restoreInto writes a backup of one instance and restores it into another.
func restoreInto(t *testing.T, source *fixture) (*fixture, RestoreResult) {
	t.Helper()

	var buf bytes.Buffer
	if err := source.svc.WriteBackup(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	target := newFixture(t)
	result, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(result.Problems) > 0 {
		t.Fatalf("restore reported problems: %v", result.Problems)
	}
	return target, result
}

// The one that matters: time that has been invoiced must not come back
// billable. Without the billing runs in the file, a restore reopens last
// quarter for billing - which is the failure the runs were built to prevent,
// reintroduced through the recovery path.
func TestRestoredTimeThatWasBilledIsStillBilled(t *testing.T) {
	source := fullInstance(t)
	target, result := restoreInto(t, source)

	if result.BillingRuns != 1 {
		t.Fatalf("restored %d billing runs, want 1", result.BillingRuns)
	}

	runs, err := target.svc.BillingRuns(target.ctx, 0, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Reference != "INV-2026-014" {
		t.Fatalf("the invoice reference did not survive: %+v", runs)
	}

	// The entry points at it, which is what makes "unbilled" true of the
	// right rows.
	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries restored, want 1", len(entries))
	}
	if entries[0].BillingRunID != runs[0].ID {
		t.Errorf("the restored entry belongs to run %d, want %d: billed time "+
			"came back billable", entries[0].BillingRunID, runs[0].ID)
	}

	// And so it is not offered for billing a second time.
	candidates, err := target.svc.BillingCandidatesFor(target.ctx, target.customer(t),
		source.now.AddDate(0, 0, -30), source.now.AddDate(0, 0, 30))
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates.Entries) != 0 {
		t.Errorf("%d entries offered for billing after a restore, want 0",
			len(candidates.Entries))
	}

	// Nor editable under the invoice.
	if _, err := target.svc.UpdateEntry(target.ctx, entries[0].ID, EntryInput{
		AssignmentID: entries[0].AssignmentID, StartedAt: entries[0].StartedAt,
		DurationSeconds: 3600,
	}); err == nil {
		t.Error("restored billed time was editable")
	}
}

func TestABackupCarriesEverythingItClaimsTo(t *testing.T) {
	source := fullInstance(t)
	target, result := restoreInto(t, source)

	for _, c := range []struct {
		what string
		got  int
	}{
		{"tags", result.Tags},
		{"contract terms", result.ContractTerms},
		{"billing runs", result.BillingRuns},
		{"routines", result.Routines},
	} {
		if c.got == 0 {
			t.Errorf("no %s were restored", c.what)
		}
	}
	// Two absences: a week's holiday and a public holiday.
	if result.Absences != 2 {
		t.Errorf("restored %d absences, want 2", result.Absences)
	}

	// The terms came back with their rate, which is what prices old work.
	terms, err := target.svc.ContractTerms(target.ctx, domain.TermsForCustomer, target.customer(t))
	if err != nil {
		t.Fatalf("read terms: %v", err)
	}
	found := false
	for _, t2 := range terms.Terms {
		if t2.EffectiveFrom == "2026-01-01" && t2.Rules.OvertimeMultiplierPct == 150 {
			found = true
		}
	}
	if !found {
		t.Error("the dated terms did not survive the round trip, so restored " +
			"work is priced against today's defaults rather than the terms in force")
	}

	// The entry kept its tags.
	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 || len(entries[0].Tags) != 2 {
		t.Errorf("the restored entry carries %v, want two tags",
			func() any {
				if len(entries) == 0 {
					return "no entry"
				}
				return entries[0].Tags
			}())
	}

	// And the leave fills its share of the week, which is the thing absence is
	// for: a restored week off must not read as a week nobody filled in.
	away, err := target.svc.absentDaysFor(target.ctx, target.user.ID,
		time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("absent days: %v", err)
	}
	if len(away) != 5 {
		t.Errorf("%d days away after the restore, want 5", len(away))
	}
}

// Restoring the same file twice must not double any of it, which is the
// property that makes a mistaken restore safe. It was already true of entries;
// it has to be true of everything now carried beside them.
func TestRestoringTheNewRecordsTwiceChangesNothing(t *testing.T) {
	source := fullInstance(t)

	var buf bytes.Buffer
	if err := source.svc.WriteBackup(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	target := newFixture(t)
	if _, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("first restore: %v", err)
	}
	second, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}

	for _, c := range []struct {
		what string
		got  int
	}{
		{"tags", second.Tags},
		{"contract terms", second.ContractTerms},
		{"billing runs", second.BillingRuns},
		{"absences", second.Absences},
		{"routines", second.Routines},
	} {
		if c.got != 0 {
			t.Errorf("the second restore created %d %s; restoring twice must "+
				"not double anything", c.got, c.what)
		}
	}

	runs, err := target.svc.BillingRuns(target.ctx, 0, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("%d billing runs after restoring twice, want 1: a duplicated "+
			"run is a customer invoiced twice in the record", len(runs))
	}
}

// A week that was submitted comes back submitted.
//
// Without the periods in the file a restored instance believes nothing was
// ever submitted, so every locked week unlocks and its entries become
// editable - the week lock undone by the recovery path rather than by anybody
// deciding to reopen anything.
func TestARestoredWeekIsStillLocked(t *testing.T) {
	source := newFixture(t)
	if _, err := source.svc.CreateEntry(source.ctx, EntryInput{
		AssignmentID: source.assignment.ID, StartedAt: source.now,
		DurationSeconds: 3600, Note: "Submitted work",
	}); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := source.svc.SubmitWeek(source.ctx, source.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	target, result := restoreInto(t, source)
	if result.Periods != 1 {
		t.Fatalf("restored %d weeks, want 1", result.Periods)
	}

	view, err := target.svc.Period(target.ctx, source.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if !view.Period.Locked() {
		t.Errorf("the restored week is %q, want a locked one: a submitted "+
			"timesheet that unlocks on restore is the lock undone by the "+
			"recovery path", view.Period.Status)
	}

	// And the entry inside it is refused, which is what the lock is for.
	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1", len(entries))
	}
	if _, err := target.svc.UpdateEntry(target.ctx, entries[0].ID, EntryInput{
		AssignmentID: entries[0].AssignmentID, StartedAt: entries[0].StartedAt,
		DurationSeconds: 7200,
	}); err == nil {
		t.Error("an entry in a restored submitted week was editable")
	}
}
