package service

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Reading somebody else's records, and taking a backup of part of the data.

// TestABackupNarrowedToAProjectAndAPeriod.
//
// A backup handed to a customer, or taken before archiving an engagement.
// Getting the bounds wrong in either direction is bad in a different way: too
// narrow loses records somebody believes they have, too wide puts one
// customer's work in another's hands.
func TestABackupNarrowedToAProjectAndAPeriod(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	// A second customer, with its own work, which must not appear.
	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Globex", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create a second customer: %v", err)
	}
	otherProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: other.ID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create a second project: %v", err)
	}
	otherAssignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: otherProject.ID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create a second assignment: %v", err)
	}

	// Work on both, in and out of the window.
	inWindow := mustCreate(t, f, at(f.now, 9, 0), 3600)
	long := f.now.AddDate(0, -6, 0)
	end := at(long, 10, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(long, 9, 0), EndedAt: &end,
		Note: "six months ago", Billable: true,
	}); err != nil {
		t.Fatalf("record old work: %v", err)
	}
	theirEnd := at(f.now, 12, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: otherAssignment.ID, StartedAt: at(f.now, 11, 0),
		EndedAt: &theirEnd, Note: "the other customer's work", Billable: true,
	}); err != nil {
		t.Fatalf("record the other customer's work: %v", err)
	}

	narrowed, err := f.svc.CreateBackup(f.ctx, BackupOptions{
		CustomerID: project.CustomerID,
		ProjectID:  project.ID,
		From:       f.now.AddDate(0, 0, -7),
		To:         f.now.AddDate(0, 0, 7),
	})
	if err != nil {
		t.Fatalf("narrowed backup: %v", err)
	}

	var notes []string
	for _, entry := range narrowed.TimeEntries {
		notes = append(notes, entry.Note)
	}
	joined := strings.Join(notes, " | ")
	if len(narrowed.TimeEntries) != 1 {
		t.Errorf("the narrowed backup holds %d entries (%s), want only the one in "+
			"the window on that project", len(narrowed.TimeEntries), joined)
	}
	if strings.Contains(joined, "six months ago") {
		t.Error("the backup includes work from outside the window it was narrowed to")
	}
	if strings.Contains(joined, "the other customer's work") {
		t.Error("a backup narrowed to one customer carries another customer's work")
	}
	_ = inWindow

	// The catalogue is narrowed too, so the archive does not name a customer
	// whose records it does not carry.
	for _, customer := range narrowed.Customers {
		if customer.Name == "Globex" {
			t.Error("the narrowed backup names a customer it holds no records for")
		}
	}

	// And a backup of everything holds everything, so the narrowing above is
	// the narrowing and not a bug.
	whole, err := f.svc.CreateBackup(f.ctx, BackupOptions{})
	if err != nil {
		t.Fatalf("whole backup: %v", err)
	}
	if len(whole.TimeEntries) != 3 {
		t.Errorf("the whole backup holds %d entries, want all 3", len(whole.TimeEntries))
	}

	// A window with nothing in it is an empty archive rather than an error:
	// "nothing happened that month" is a true answer.
	empty, err := f.svc.CreateBackup(f.ctx, BackupOptions{
		From: f.now.AddDate(1, 0, 0), To: f.now.AddDate(1, 0, 7),
	})
	if err != nil {
		t.Fatalf("empty backup: %v", err)
	}
	if len(empty.TimeEntries) != 0 {
		t.Errorf("a window with no work in it holds %d entries", len(empty.TimeEntries))
	}
}

// TestRestoringABackupWrittenByAnOlderVersion.
//
// A backup format that abandons its own old files is not a backup format. A
// document from before a field existed arrives with it empty, and the restore
// has to supply what the column now requires rather than refusing the row -
// otherwise upgrading the application makes last year's backups unreadable.
func TestRestoringABackupWrittenByAnOlderVersion(t *testing.T) {
	f := newFixture(t)

	started := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	// No status and no time zone on the entry, and no status on the expense:
	// the shape of a document written before those columns existed.
	document := damagedBackup(t, Backup{
		Customers: []domain.Customer{{Name: "Globex", Currency: "EUR"}},
		Projects: []domain.Project{
			{CustomerName: "Globex", Name: "Rollout", BillableDefault: true},
		},
		Assignments: []domain.Assignment{
			{CustomerName: "Globex", ProjectName: "Rollout", Name: "Support",
				BillableDefault: true},
		},
		TimeEntries: []BackupEntry{{
			Customer: "Globex", Project: "Rollout", Assignment: "Support",
			StartedAt: started, EndedAt: timePtr(started.Add(time.Hour)),
			Seconds: 3600, Billable: true,
		}},
		Expenses: []BackupExpense{{
			Customer: "Globex", Project: "Rollout", SpentOn: "2026-03-16",
			Category: "travel", Description: "Taxi", AmountMinor: 10000,
			Currency: "EUR",
		}},
	})

	result, err := f.svc.Restore(f.ctx, bytes.NewReader(document))
	if err != nil {
		t.Fatalf("restore an older document: %v", err)
	}
	if result.Entries != 1 {
		t.Errorf("restored %d entries, want 1 (problems: %v)",
			result.Entries, result.Problems)
	}
	if result.Expenses != 1 {
		t.Errorf("restored %d expenses, want 1 (problems: %v)",
			result.Expenses, result.Problems)
	}

	// The defaults are the ones that make the record count: confirmed work in
	// a known zone, rather than a pending entry nobody sees.
	entries, err := f.svc.Entries(f.ctx, EntryFilter{
		From: started.AddDate(0, 0, -1), To: started.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries after the restore, want 1", len(entries))
	}
	if entries[0].Status != domain.StatusConfirmed {
		t.Errorf("the restored entry's status is %q, want confirmed - a pending "+
			"entry counts towards nothing and waits for a decision nobody will make",
			entries[0].Status)
	}
	if entries[0].TimeZone == "" {
		t.Error("the restored entry has no time zone, so which calendar day it " +
			"falls on is undefined")
	}
	if !entries[0].Counts() {
		t.Error("the restored entry does not count towards a total")
	}
}

// TestAnExpenseIsPricedInItsCustomersCurrency.
//
// An amount is typed without a currency: the screen shows one engagement at a
// time, and "100" means a hundred of whatever that customer is invoiced in. So
// the currency comes from the project, and the same figure under two customers
// is two different amounts of money.
//
// Getting this from the input instead would mean a form that forgot the field
// priced a claim in the wrong currency, which no total would reveal - the
// numbers would simply be wrong by an exchange rate.
func TestAnExpenseIsPricedInItsCustomersCurrency(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	// A second customer, invoiced in something else.
	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Globex", Currency: "JPY"})
	if err != nil {
		t.Fatalf("create a second customer: %v", err)
	}
	otherProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: other.ID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create a second project: %v", err)
	}

	swedish, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi", Amount: "100", Billable: true,
	})
	if err != nil {
		t.Fatalf("record against the first customer: %v", err)
	}
	if swedish.Currency != "SEK" {
		t.Errorf("the expense is in %q, want the customer's SEK", swedish.Currency)
	}
	if swedish.AmountMinor != 10000 {
		t.Errorf("100 SEK came to %d minor units, want 10000 - two decimal places",
			swedish.AmountMinor)
	}

	// The currency travels with the amount, and so does how many minor units a
	// decimal place is worth. A yen has no subdivision, so a hundred yen is a
	// hundred minor units and not ten thousand - which is what this held for a
	// long time, because every currency was parsed at two decimal places.
	// ADR-0014's "cents, öre, pence" is the model; it is not a claim that every
	// currency has them.
	japanese, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: otherProject.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi", Amount: "100", Billable: true,
	})
	if err != nil {
		t.Fatalf("record against the second customer: %v", err)
	}
	if japanese.Currency != "JPY" {
		t.Errorf("the expense is in %q, want the customer's JPY", japanese.Currency)
	}
	if japanese.AmountMinor != 100 {
		t.Errorf("100 JPY came to %d minor units, want 100 - a yen has no "+
			"subdivision to hold the other two decimal places",
			japanese.AmountMinor)
	}

	// An edit keeps the currency, and reparses the amount in it.
	edited, err := f.svc.UpdateExpense(f.ctx, japanese.ID, ExpenseInput{
		ProjectID: otherProject.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi (corrected)", Amount: "250", Billable: true,
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Currency != "JPY" {
		t.Errorf("the edited expense is in %q, want the JPY it was recorded in",
			edited.Currency)
	}
	if edited.AmountMinor != 250 {
		t.Errorf("the edited amount is %d minor units; 250 yen is 250 of them",
			edited.AmountMinor)
	}

	// An amount that is not one is refused rather than priced at zero.
	if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Amount: "a hundred or so",
	}); err == nil {
		t.Error("an expense was recorded from an amount in words")
	}
}

// TestAManagerReadsTheirTeamsTimesheets.
//
// Looking at somebody else's week is what an approval is. It is permitted by
// role and bounded by membership, so a manager sees their team and not the
// company - and a member asking for a colleague's timesheet is refused.
func TestAManagerReadsTheirTeamsTimesheets(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	theirs := f.asUser(colleague)
	end := time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC)
	if _, err := f.svc.CreateEntry(theirs, EntryInput{
		AssignmentID: assignment.ID,
		StartedAt:    time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end, Billable: true, Note: "their day's work",
	}); err != nil {
		t.Fatalf("record their time: %v", err)
	}

	// The administrator, who may approve, can read it by naming them.
	listed, err := f.svc.Entries(f.ctx, EntryFilter{UserID: colleague.ID})
	if err != nil {
		t.Fatalf("read their timesheet: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("%d entries for the colleague, want 1", len(listed))
	}
	if listed[0].Note != "their day's work" {
		t.Errorf("the entry reads %q", listed[0].Note)
	}

	// Their expenses too, through the same door.
	if _, err := f.svc.CreateExpense(theirs, ExpenseInput{
		ProjectID: assignment.ProjectID, SpentOn: "2026-03-16",
		Category: "travel", Description: "their taxi", Amount: "100",
	}); err != nil {
		t.Fatalf("record their expense: %v", err)
	}
	expenses, err := f.svc.Expenses(f.ctx, ExpenseFilter{UserID: colleague.ID})
	if err != nil {
		t.Fatalf("read their expenses: %v", err)
	}
	if len(expenses) != 1 {
		t.Errorf("%d expenses for the colleague, want 1", len(expenses))
	}

	// A member naming a colleague sees that colleague's work *on the projects
	// they share*, which is the role model doing what it says: "members read
	// their own records and the projects they belong to". So this is allowed.
	adminEnd := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID,
		StartedAt:    time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		EndedAt:      &adminEnd, Note: "work on the shared project",
	}); err != nil {
		t.Fatalf("record the administrator's own time: %v", err)
	}
	shared, err := f.svc.Entries(theirs, EntryFilter{UserID: f.admin.ID})
	if err != nil {
		t.Fatalf("a member reading a colleague on a shared project: %v", err)
	}
	if len(shared) != 1 || shared[0].Note != "work on the shared project" {
		t.Errorf("a member sees %d of their colleague's entries on the project "+
			"they share, want the 1 recorded there", len(shared))
	}

	// Somebody on no projects sees nothing of theirs, which is where the scope
	// rather than the authoriser does the work: the listing is narrowed by
	// effectiveScope after the permission check, so a permissive check cannot
	// turn into a disclosure here.
	//
	// It is belt and braces with the authoriser rather than a substitute for
	// it: Entry, Customer and AuditTrail return a record directly with no
	// scope applied after, so they depend on the check alone - see
	// TestReadsAreScopedByProjectMembership.
	stranger, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Stranger", Email: "stranger@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create a stranger: %v", err)
	}
	none, err := f.svc.Entries(f.asUser(stranger), EntryFilter{UserID: f.admin.ID})
	if err == nil && len(none) != 0 {
		t.Errorf("somebody on no projects read %d of another user's entries", len(none))
	}
	noExpenses, err := f.svc.Expenses(f.asUser(stranger), ExpenseFilter{UserID: f.admin.ID})
	if err == nil && len(noExpenses) != 0 {
		t.Errorf("somebody on no projects read %d of another user's expenses",
			len(noExpenses))
	}

	// Naming themselves is always allowed, which is the same code path taking
	// its early exit.
	if _, err := f.svc.Entries(theirs, EntryFilter{UserID: colleague.ID}); err != nil {
		t.Errorf("a member could not read their own timesheet by naming themselves: %v", err)
	}
}
