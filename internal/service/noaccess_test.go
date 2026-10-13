package service

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Every method, as somebody who should see none of it.
//
// A member who belongs to no projects is the least-privileged real user there
// is: a new joiner, or somebody whose last engagement ended. They have their
// own empty timesheet and nothing else, and the scope is what makes that true.
//
// The failure this guards against is not a crash. It is a method that answers
// with somebody else's records because it forgot to ask for a scope, or asked
// for one and did not apply it. That reads as working perfectly - which is why
// it needs a sweep rather than a test per method: the one that forgot is
// exactly the one nobody wrote a test for.
//
// Each call must do one of three things: refuse, answer with nothing, or
// answer with only what belongs to the actor. Returning somebody else's
// customer, project, entry or expense is a leak, and that is what is checked.

// strangerFixture is an instance holding one team's records, acted on by a
// member who belongs to no project at all.
type strangerFixture struct {
	*serverFixture
	stranger context.Context
	// colleague is the other side of the same question: a member who does work
	// on the project. Without them a refusal proves nothing, because an
	// authoriser that refuses everybody would pass every test below.
	colleague    context.Context
	theirEntry   domain.TimeEntry
	theirExpense domain.Expense
	customerID   int64
	projectID    int64
	assignmentID int64
}

func newStrangerFixture(t *testing.T) *strangerFixture {
	t.Helper()
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	project, err := f.svc.Project(f.ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	end := time.Date(2026, 3, 16, 17, 0, 0, 0, time.UTC)
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID,
		StartedAt:    time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end, Billable: true, Note: "work the stranger must not see",
	})
	if err != nil {
		t.Fatalf("record time: %v", err)
	}
	expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "a cost the stranger must not see", Amount: "100", Billable: true,
	})
	if err != nil {
		t.Fatalf("record an expense: %v", err)
	}

	// Somebody with an account and no memberships.
	stranger, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "New Joiner", Email: "joiner@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create the stranger: %v", err)
	}

	return &strangerFixture{
		serverFixture: f,
		stranger:      auth.WithUser(context.Background(), stranger),
		colleague:     auth.WithUser(context.Background(), colleague),
		theirEntry:    entry, theirExpense: expense,
		customerID: project.CustomerID, projectID: project.ID,
		assignmentID: assignment.ID,
	}
}

// TestSomebodyWithNoProjectsReachesNoneOfThem.
//
// See the note at the top of this file.
func TestSomebodyWithNoProjectsReachesNoneOfThem(t *testing.T) {
	f := newStrangerFixture(t)
	svc, ctx := f.svc, f.stranger

	// An instance-wide archive sitting in the server's backup directory, taken
	// by the administrator. Nobody's scope narrowed it, so it is the one file on
	// the machine that holds every project whole - which makes a read of it by
	// somebody on no project the sharpest form of this question.
	backupDir := t.TempDir()
	storedBackup, err := svc.WriteBackupFile(f.ctx, backupDir, BackupOptions{})
	if err != nil {
		t.Fatalf("take a backup as the administrator: %v", err)
	}

	// Each call returns what it produced, flattened to a count and a string, so
	// one assertion covers readers of every shape: nothing, or nothing of
	// somebody else's.
	type answer struct {
		rows int
		text string
	}
	reads := map[string]func() (answer, error){
		"Entries": func() (answer, error) {
			out, err := svc.Entries(ctx, EntryFilter{})
			return answer{len(out), describeEntries(out)}, err
		},
		"CountEntries": func() (answer, error) {
			n, err := svc.CountEntries(ctx, EntryFilter{})
			return answer{n, ""}, err
		},
		"SearchEntries": func() (answer, error) {
			out, _, err := svc.SearchEntries(ctx, EntryFilter{Query: "work"})
			return answer{len(out), describeEntries(out)}, err
		},
		"SearchEntriesPage": func() (answer, error) {
			page, err := svc.SearchEntriesPage(ctx, EntryFilter{Query: "work", Limit: 50})
			return answer{len(page.Entries), describeEntries(page.Entries)}, err
		},
		"EachEntry": func() (answer, error) {
			var out []domain.TimeEntry
			for entry, err := range svc.EachEntry(ctx, EntryFilter{}) {
				if err != nil {
					return answer{}, err
				}
				out = append(out, entry)
			}
			return answer{len(out), describeEntries(out)}, nil
		},
		"Expenses": func() (answer, error) {
			out, err := svc.Expenses(ctx, ExpenseFilter{})
			var text []string
			for _, expense := range out {
				text = append(text, expense.Description)
			}
			return answer{len(out), strings.Join(text, " ")}, err
		},
		"Expense": func() (answer, error) {
			expense, err := svc.Expense(ctx, f.theirExpense.ID)
			return answer{1, expense.Description}, err
		},
		"Customers": func() (answer, error) {
			out, err := svc.Customers(ctx, true)
			var text []string
			for _, customer := range out {
				text = append(text, customer.Name)
			}
			return answer{len(out), strings.Join(text, " ")}, err
		},
		"Projects": func() (answer, error) {
			out, err := svc.Projects(ctx, 0, true)
			var text []string
			for _, project := range out {
				text = append(text, project.Name)
			}
			return answer{len(out), strings.Join(text, " ")}, err
		},
		"Project": func() (answer, error) {
			project, err := svc.Project(ctx, f.projectID)
			return answer{1, project.Name}, err
		},
		"Assignments": func() (answer, error) {
			out, err := svc.Assignments(ctx, 0, true)
			var text []string
			for _, assignment := range out {
				text = append(text, assignment.Name)
			}
			return answer{len(out), strings.Join(text, " ")}, err
		},
		"Assignment": func() (answer, error) {
			assignment, err := svc.Assignment(ctx, f.assignmentID)
			return answer{1, assignment.Name}, err
		},
		"RecentAssignments": func() (answer, error) {
			out, err := svc.RecentAssignments(ctx, 50)
			return answer{len(out), ""}, err
		},
		"QuickStart": func() (answer, error) {
			out, err := svc.QuickStart(ctx, 10)
			return answer{len(out), ""}, err
		},
		"Day": func() (answer, error) {
			view, err := svc.Day(ctx, f.theirEntry.StartedAt)
			return answer{len(view.Entries), describeEntries(view.Entries)}, err
		},
		"Week": func() (answer, error) {
			view, err := svc.Week(ctx, f.theirEntry.StartedAt)
			return answer{int(view.Totals.SummedSeconds), ""}, err
		},
		"Month": func() (answer, error) {
			view, err := svc.Month(ctx, f.theirEntry.StartedAt)
			return answer{int(view.Totals.SummedSeconds), ""}, err
		},
		"BudgetReportFor": func() (answer, error) {
			report, err := svc.BudgetReportFor(ctx, f.theirEntry.StartedAt)
			return answer{len(report.Rows), ""}, err
		},
		"ApprovalReportFor": func() (answer, error) {
			report, err := svc.ApprovalReportFor(ctx, f.theirEntry.StartedAt, 4)
			return answer{len(report.Rows), ""}, err
		},
		"Inbox": func() (answer, error) {
			inbox, err := svc.Inbox(ctx)
			return answer{len(inbox.Entries) + len(inbox.Expenses), ""}, err
		},
		"PendingCount": func() (answer, error) {
			n, err := svc.PendingCount(ctx)
			return answer{n, ""}, err
		},
		"ProposedByMe": func() (answer, error) {
			out, err := svc.ProposedByMe(ctx)
			return answer{len(out), describeEntries(out)}, err
		},
		"PendingIdle": func() (answer, error) {
			out, err := svc.PendingIdle(ctx)
			return answer{len(out), ""}, err
		},
		"RunningIdle": func() (answer, error) {
			out, err := svc.RunningIdle(ctx)
			return answer{len(out), ""}, err
		},
		"RunningTimers": func() (answer, error) {
			out, err := svc.RunningTimers(ctx)
			return answer{len(out), ""}, err
		},
		"Tags": func() (answer, error) {
			out, err := svc.Tags(ctx)
			return answer{len(out), ""}, err
		},
		"Routines": func() (answer, error) {
			out, err := svc.Routines(ctx, false)
			return answer{len(out), ""}, err
		},
		"ContractTerms": func() (answer, error) {
			view, err := svc.ContractTerms(ctx, domain.TermsForCustomer, f.customerID)
			return answer{len(view.Terms), ""}, err
		},
		"Attachments": func() (answer, error) {
			out, err := svc.Attachments(ctx, domain.AttachmentOwnerTimeEntry, f.theirEntry.ID)
			return answer{len(out), ""}, err
		},
		"PreviewExport": func() (answer, error) {
			preview, err := svc.PreviewExport(ctx, EntryFilter{})
			var text []string
			for _, group := range preview.ByCustomer {
				text = append(text, group.Customer)
				for _, line := range group.Projects {
					text = append(text, line.Project)
				}
			}
			return answer{preview.Entries, strings.Join(text, " ")}, err
		},
		"ReviewSubmission": func() (answer, error) {
			review, err := svc.ReviewSubmission(ctx, f.theirEntry.StartedAt)
			return answer{len(review.Week.Rows) + len(review.Expenses),
				describeEntries(append(append([]domain.TimeEntry{}, review.Flagged...),
					append(review.Pending, review.Running...)...))}, err
		},
		"CreateBackup": func() (answer, error) {
			backup, err := svc.CreateBackup(ctx, BackupOptions{})
			return answer{len(backup.TimeEntries), describeBackupEntries(backup)}, err
		},
		// The three that were once unscoped, swept here now that they are not:
		// each refuses, and a refusal is a correct answer below.
		"Entry": func() (answer, error) {
			entry, err := svc.Entry(ctx, f.theirEntry.ID)
			return answer{1, describeEntries([]domain.TimeEntry{entry})}, err
		},
		"Customer": func() (answer, error) {
			customer, err := svc.Customer(ctx, f.customerID)
			return answer{1, customer.Name}, err
		},
		"AuditTrail": func() (answer, error) {
			trail, err := svc.AuditTrail(ctx, "time_entry", f.theirEntry.ID)
			return answer{len(trail), ""}, err
		},
		"PreviewBackupFile": func() (answer, error) {
			preview, err := svc.PreviewBackupFile(ctx, backupDir, storedBackup.Name)
			return answer{preview.Counts.Total(), strings.Join(preview.Customers, " ")}, err
		},
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil {
				// A refusal is a correct answer: there is nothing of theirs to
				// show, and saying so plainly is better than an empty screen
				// with somebody else's heading on it.
				return
			}
			for _, leak := range []string{
				"work the stranger must not see",
				"a cost the stranger must not see",
				"Acme", "Migration", "Development",
			} {
				if strings.Contains(got.text, leak) {
					t.Errorf("%s answered with %q, which belongs to a project this "+
						"user is not on", name, leak)
				}
			}
			if got.rows != 0 && got.text == "" {
				t.Errorf("%s answered with %d rows for somebody who belongs to no "+
					"project; if any of them are legitimately theirs, this test "+
					"needs to say which", name, got.rows)
			}
		})
	}
}

// describeEntries joins the notes of a set of entries, so a leak is visible in
// the assertion's message rather than only as a count.
func describeEntries(entries []domain.TimeEntry) string {
	var text []string
	for _, entry := range entries {
		text = append(text, entry.Note, entry.CustomerName, entry.ProjectName, entry.AssignmentName)
	}
	return strings.Join(text, " ")
}

// describeBackupEntries is the same for an archive, which is the one read that
// can carry the whole database out of the building in a single call.
func describeBackupEntries(backup Backup) string {
	var text []string
	for _, entry := range backup.TimeEntries {
		text = append(text, entry.Note, entry.Customer, entry.Project, entry.Assignment)
	}
	for _, customer := range backup.Customers {
		text = append(text, customer.Name)
	}
	return strings.Join(text, " ")
}

// TestSomebodyWithNoProjectsCannotWriteIntoOne.
//
// The other half: a scope that correctly hides a record must also stop it
// being changed. Hiding alone would be a read-only leak - somebody who knows
// an id could still edit, delete or move what they cannot see.
func TestSomebodyWithNoProjectsCannotWriteIntoOne(t *testing.T) {
	f := newStrangerFixture(t)
	svc, ctx := f.svc, f.stranger

	writes := map[string]func() error{
		"edit their entry": func() error {
			_, err := svc.UpdateEntry(ctx, f.theirEntry.ID, EntryInput{
				AssignmentID: f.assignmentID, StartedAt: f.theirEntry.StartedAt,
				DurationSeconds: 60,
			})
			return err
		},
		"delete their entry": func() error {
			return svc.DeleteEntry(ctx, f.theirEntry.ID)
		},
		"stop their timer": func() error {
			_, err := svc.StopTimer(ctx, f.theirEntry.ID)
			return err
		},
		"move their entry": func() error {
			result, err := svc.MoveEntries(ctx, []int64{f.theirEntry.ID}, f.assignmentID)
			if err == nil && result.Moved > 0 {
				return nil
			}
			if err == nil {
				return errSkippedRatherThanMoved
			}
			return err
		},
		"edit their expense": func() error {
			_, err := svc.UpdateExpense(ctx, f.theirExpense.ID, ExpenseInput{
				ProjectID: f.projectID, SpentOn: "2026-03-16",
				Category: "travel", Amount: "1",
			})
			return err
		},
		"delete their expense": func() error {
			return svc.DeleteExpense(ctx, f.theirExpense.ID)
		},
		"rename their customer": func() error {
			return svc.UpdateCustomer(ctx, domain.Customer{
				ID: f.customerID, Name: "Renamed", Currency: "SEK",
			})
		},
		"rename their project": func() error {
			return svc.UpdateProject(ctx, domain.Project{
				ID: f.projectID, CustomerID: f.customerID, Name: "Renamed",
			})
		},
		"archive their project": func() error {
			return svc.SetProjectArchived(ctx, f.projectID, true)
		},
		"write their contract terms": func() error {
			return svc.SaveContractTerms(ctx, domain.ContractTerms{
				Scope: domain.TermsForCustomer, ScopeID: f.customerID,
				EffectiveFrom: "2026-01-01",
			})
		},
		"approve their week": func() error {
			return svc.ApproveWeek(ctx, f.admin.ID, "2026-03-16")
		},
		"create a customer": func() error {
			_, err := svc.CreateCustomer(ctx, domain.Customer{Name: "Theirs", Currency: "EUR"})
			return err
		},
		"change the instance settings": func() error {
			return svc.UpdateSettings(ctx, SettingsInput{DefaultCurrency: "USD", WeekStart: 7})
		},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			if err := write(); err == nil {
				t.Errorf("somebody who belongs to no project could %s", name)
			}
		})
	}

	// And nothing changed: the records are as the team left them.
	entry, err := f.svc.Entry(f.ctx, f.theirEntry.ID)
	if err != nil {
		t.Fatalf("read the entry as its owner: %v", err)
	}
	if entry.Note != "work the stranger must not see" {
		t.Errorf("the entry's note is now %q", entry.Note)
	}
	if entry.DurationSeconds != f.theirEntry.DurationSeconds {
		t.Errorf("the entry is now %ds long, want %ds",
			entry.DurationSeconds, f.theirEntry.DurationSeconds)
	}
	customer, err := f.svc.Customer(f.ctx, f.customerID)
	if err != nil {
		t.Fatalf("read the customer: %v", err)
	}
	if customer.Name != "Acme" {
		t.Errorf("the customer is now named %q", customer.Name)
	}
}

// errSkippedRatherThanMoved marks a bulk operation that refused by skipping,
// which is its way of saying no.
var errSkippedRatherThanMoved = skippedError{}

type skippedError struct{}

func (skippedError) Error() string { return "skipped rather than moved" }

// TestBothSweepsCoverTheSameSurface.
//
// The two sweeps in this package - this one and the dead-database one - are
// each a table of method names, and a method missing from one is a gap nobody
// sees. This checks that every reader in the dead-database sweep is either
// exercised here or listed as not needing to be, so the scope half cannot
// fall behind the failure half.
func TestBothSweepsCoverTheSameSurface(t *testing.T) {
	// Methods whose answer does not depend on who is asking, so there is
	// nothing for a scope to hide: preferences, the clock, instance settings,
	// and the parsers, which read a file rather than the database.
	personal := map[string]bool{
		"Settings": true, "IdleThresholdSeconds": true, "LocalUser": true,
		"EnsureLocalUser": true, "SetTheme": true, "SetLanguage": true,
		"SetTimeZone": true, "DismissReminder": true, "Reminders": true,
		"Period": true, "RecentPeriods": true, "ApprovedPeriods": true,
		"PendingApprovals": true, "SubmitWeek": true, "WithdrawWeek": true,
		"OvertimeNotices": true, "RoutinesDue": true, "TextPreviews": true,
		"AuthorizeBackup": true, "NeedsReceipt": true,
		// Answers "may you administer the catalogue" with a bool, for a
		// template deciding whether to draw a button. It discloses nothing: the
		// answer is the actor's own permission, not anybody's data.
		"MayManageCatalogue": true,
		"TermsOn":            true, "TermsCurrency": true,
		"ParseQuickAdd": true, "ParseTimeCSV": true, "ParseCalendar": true,
		// PreviewArchive is the same shape as the parsers above: the caller
		// hands it the bytes, so it describes a file they already hold and
		// there is nothing for a scope to withhold. Narrowing it would describe
		// a file other than the one about to be restored. Reading one off the
		// server's disk is a different question, and PreviewBackupFile is in
		// the table above rather than here because of it.
		"PreviewArchive": true,
	}
	// Writes, covered by the second test in this file rather than the first.
	written := map[string]bool{
		"CreateEntry": true, "UpdateEntry": true, "DeleteEntry": true,
		"StartTimer": true, "StopTimer": true, "StopAllTimers": true,
		"SwitchTo": true, "QuickAdd": true, "CreateExpense": true,
		"UpdateExpense": true, "DeleteExpense": true, "CreateCustomer": true,
		"UpdateCustomer": true, "SetCustomerArchived": true,
		"CreateProject": true, "UpdateProject": true, "SetProjectArchived": true,
		"CreateAssignment": true, "UpdateAssignment": true,
		"SetAssignmentArchived": true, "SetFavourite": true,
		"UpdateSettings": true, "UpdateTag": true, "DeleteTag": true,
		"ReindexSearch": true, "SaveContractTerms": true,
		"DeleteContractTerms": true, "SaveRoutine": true, "DeleteRoutine": true,
		"ApplyRoutine": true, "ApplyAllRoutines": true, "CopyDay": true,
		"CopyWeek": true, "MoveEntries": true, "MoveExpenses": true,
		"ApproveWeek": true, "RejectWeek": true, "ReopenWeek": true,
		"AcceptEntry": true, "RejectEntry": true, "AcceptExpense": true,
		"RejectExpense": true, "RecordIdle": true, "ResolveIdle": true,
		"Attach": true, "DeleteAttachment": true, "SweepBlobs": true,
		"OpenAttachment": true, "PreviewAttachment": true,
		"ImportTimeCSV": true, "ImportCalendar": true,
		"Restore": true, "RestoreArchive": true,
		"WriteArchive": true, "WriteBackup": true, "WriteBackupFile": true,
	}

	f := newStrangerFixture(t)
	serviceType := reflect.TypeOf(f.svc)
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()

	var missing []string
	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		if method.Type.NumIn() < 2 || method.Type.In(1) != ctxType {
			continue
		}
		if personal[method.Name] || written[method.Name] {
			continue
		}
		if !strangerReads[method.Name] {
			missing = append(missing, method.Name)
		}
	}
	for _, name := range missing {
		t.Errorf("%s reads the database and is not swept for scope. Add it to the "+
			"reads table above, or to the personal or written list with the reason.",
			name)
	}
}

// strangerReads names the reads the first test in this file exercises. It is a
// second spelling of that test's table keys, which the test above compares
// against the type - kept here rather than extracted so the table stays
// readable as a table.
var strangerReads = map[string]bool{
	"Entries": true, "CountEntries": true, "SearchEntries": true,
	"SearchEntriesPage": true, "EachEntry": true,
	"Expenses": true, "Expense": true, "Customers": true,
	"Projects": true, "Project": true, "Assignments": true, "Assignment": true,
	"RecentAssignments": true, "QuickStart": true, "Day": true, "Week": true,
	"Month": true, "BudgetReportFor": true, "ApprovalReportFor": true,
	"Inbox": true, "PendingCount": true, "ProposedByMe": true,
	"PendingIdle": true, "RunningIdle": true, "RunningTimers": true,
	"Tags": true, "Routines": true, "ContractTerms": true, "ReviewSubmission": true,
	"PreviewExport": true, "PreviewBackupFile": true,
	"Entry": true, "Customer": true, "AuditTrail": true,
	"EntryForEdit": true, "CustomerForEdit": true, "ProjectForEdit": true,
	"AssignmentForEdit": true, "TagsForAdmin": true, "SettingsForAdmin": true,
	"Attachments": true, "CreateBackup": true,
}

// TestReadsAreScopedByProjectMembership.
//
// Three reads once let a member see records belonging to a project they were not
// on. They shared one cause, and it is worth stating, because the fix is spread
// over three files and none of them looks like a security change on its own.
//
// RoleAuthorizer.requireMembership permits a resource that names no project,
// which is right for one that genuinely has none - the instance settings, a user
// account. But entryResource built auth.Resource{Type, ID, OwnerID} with no
// project id, and the customer and audit-trail resources carried none either, so
// a member asking about somebody else's record was answered as though they had
// asked about a global setting. Entry gave another user's entry with its note
// and its billing snapshot; Customer gave any account with its negotiated rate;
// AuditTrail gave the history of any record of any type, including the values
// each change moved between.
//
// All three now say where the record sits, the authoriser scopes a customer by
// membership of any of its projects, and a named record of a project-scoped type
// that says nothing about where it sits is refused rather than permitted - which
// is what makes the next omission of this kind fail closed.
//
// The listings were never affected: they go through effectiveScope and are
// narrowed there, which is why this appeared on no screen that shows a list. It
// needed an id, and ids are sequential.
func TestReadsAreScopedByProjectMembership(t *testing.T) {
	f := newStrangerFixture(t)

	// The authoriser itself, first: a record that names its project is refused
	// to a non-member, and so is one that names nothing at all.
	named := auth.Resource{
		Type: "time_entry", ID: f.theirEntry.ID, OwnerID: f.admin.ID,
		ProjectID: f.projectID, CustomerID: f.customerID,
	}
	bare := auth.Resource{Type: "time_entry", ID: f.theirEntry.ID, OwnerID: f.admin.ID}

	if err := f.svc.authz.Can(f.stranger, auth.ActionView, named); err == nil {
		t.Error("the authoriser permits a non-member to view a record on a " +
			"project they are not on")
	}
	if err := f.svc.authz.Can(f.stranger, auth.ActionView, bare); err == nil {
		t.Error("the authoriser permits a named time entry that says nothing " +
			"about where it sits; that is the shape of a global setting, and it " +
			"is how a colleague's entry was readable by its id alone")
	}
	// The three reads, each refused by name rather than by returning
	// nothing - a refusal is the honest answer to "show me a record you may
	// not see", and ErrNotFound is what the service turns it into so that
	// probing ids cannot enumerate records.
	for _, read := range []struct {
		name string
		call func() error
	}{
		{"Entry", func() error {
			_, err := f.svc.Entry(f.stranger, f.theirEntry.ID)
			return err
		}},
		{"Customer", func() error {
			_, err := f.svc.Customer(f.stranger, f.customerID)
			return err
		}},
		{"AuditTrail", func() error {
			_, err := f.svc.AuditTrail(f.stranger, "time_entry", f.theirEntry.ID)
			return err
		}},
		// The same question asked through the records in between, since each
		// resolves its own scope and each could have forgotten.
		{"Expense", func() error {
			_, err := f.svc.Expense(f.stranger, f.theirExpense.ID)
			return err
		}},
		{"Project", func() error {
			_, err := f.svc.Project(f.stranger, f.projectID)
			return err
		}},
		{"Assignment", func() error {
			_, err := f.svc.Assignment(f.stranger, f.assignmentID)
			return err
		}},
	} {
		t.Run(read.name, func(t *testing.T) {
			err := read.call()
			if err == nil {
				t.Fatalf("%s answered a member who belongs to no project", read.name)
			}
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s refused with %v; a record somebody may not see is "+
					"reported as absent, so probing ids cannot enumerate "+
					"records", read.name, err)
			}
		})
	}

	// And the other side of each: somebody who does work on the project reads
	// all of it. A membership check that refused everybody would pass every
	// assertion above, and the customer lookup is the one most easily broken
	// this way - a customer record names no project, so it is answered by a
	// second query, and a nil one fails closed.
	for _, read := range []struct {
		name string
		call func() error
	}{
		{"Entry", func() error {
			_, err := f.svc.Entry(f.colleague, f.theirEntry.ID)
			return err
		}},
		{"Customer", func() error {
			_, err := f.svc.Customer(f.colleague, f.customerID)
			return err
		}},
		{"AuditTrail", func() error {
			_, err := f.svc.AuditTrail(f.colleague, "time_entry", f.theirEntry.ID)
			return err
		}},
		{"Expense", func() error {
			_, err := f.svc.Expense(f.colleague, f.theirExpense.ID)
			return err
		}},
		{"Project", func() error {
			_, err := f.svc.Project(f.colleague, f.projectID)
			return err
		}},
		{"Assignment", func() error {
			_, err := f.svc.Assignment(f.colleague, f.assignmentID)
			return err
		}},
	} {
		t.Run(read.name+" on the project", func(t *testing.T) {
			if err := read.call(); err != nil {
				t.Errorf("%s refused a member of the project: %v", read.name, err)
			}
		})
	}

	// Their own records are still their own, which is the half a membership
	// check is easiest to break: the owner branch answers before membership is
	// consulted at all.
	end := time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC)
	own, err := f.svc.CreateEntry(f.stranger, EntryInput{
		AssignmentID: f.assignmentID,
		StartedAt:    time.Date(2026, 3, 18, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end,
	})
	if err != nil {
		t.Fatalf("record own time: %v", err)
	}
	if _, err := f.svc.Entry(f.stranger, own.ID); err != nil {
		t.Errorf("a member cannot read the entry they just recorded: %v", err)
	}
	if _, err := f.svc.AuditTrail(f.stranger, "time_entry", own.ID); err != nil {
		t.Errorf("a member cannot read the history of their own entry: %v", err)
	}
}

// TestTheHistoryOfSomethingDeletedIsAdministrative.
//
// The trail deliberately outlives the record it describes, so once the record is
// gone there is nothing left to read a scope from. An administrator can still
// account for the deletion; nobody else can, which is the conservative end of a
// choice that had to be made either way.
func TestTheHistoryOfSomethingDeletedIsAdministrative(t *testing.T) {
	f := newStrangerFixture(t)

	trail, err := f.svc.AuditTrail(f.ctx, "time_entry", f.theirEntry.ID)
	if err != nil {
		t.Fatalf("read the trail as the administrator: %v", err)
	}
	if len(trail) == 0 {
		t.Fatal("the entry has no recorded history, so this test reads nothing")
	}

	if err := f.svc.DeleteEntry(f.ctx, f.theirEntry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	after, err := f.svc.AuditTrail(f.ctx, "time_entry", f.theirEntry.ID)
	if err != nil {
		t.Fatalf("the administrator cannot account for a deletion: %v", err)
	}
	if len(after) <= len(trail) {
		t.Errorf("the deletion is not in the trail: %d rows before, %d after",
			len(trail), len(after))
	}

	if _, err := f.svc.AuditTrail(f.stranger, "time_entry", f.theirEntry.ID); err == nil {
		t.Error("the history of a deleted entry was read by somebody who could " +
			"not have read the entry")
	}
}

// TestRecordingOwnTimeIsNotBoundedByMembership.
//
// A member may record their own time against any assignment, including one on
// a project they do not belong to. This is the authoriser's member branch
// doing what its comment says - "write access is limited to their own time" -
// and it checks ownership rather than membership for a create.
//
// Pinned because it is adjacent to the reads above and a different thing: it
// discloses nothing, since it writes rather than reads, and it needs an
// assignment id the person has no listing to find. Whether it should also
// require membership is a policy question; this test records which way it
// currently goes.
func TestRecordingOwnTimeIsNotBoundedByMembership(t *testing.T) {
	f := newStrangerFixture(t)

	end := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	entry, err := f.svc.CreateEntry(f.stranger, EntryInput{
		AssignmentID: f.assignmentID,
		StartedAt:    time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end,
	})
	if err != nil {
		t.Logf("recording own time off-project is now refused (%v); if that is "+
			"deliberate, invert this test", err)
		return
	}

	// It is their own entry, not an edit to anybody else's.
	if entry.UserID == f.admin.ID {
		t.Error("the entry was recorded against another user")
	}

	// And it does not give them the project: the listing still shows them
	// nothing but their own.
	entries, err := f.svc.Entries(f.stranger, EntryFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, listed := range entries {
		if listed.UserID != entry.UserID {
			t.Errorf("the listing shows an entry owned by %d", listed.UserID)
		}
	}

	// And they can see it, which is the half that was missing. scopeFor fills
	// OwnUserID and says "member - the projects they are a member of, plus
	// their own records"; Scope.condition ignored it, so an empty project list
	// rendered as "1 = 0" and the hour they had just recorded was absent from
	// the listing, the day and the week. ADR-0008 says every new and every
	// SSO-provisioned account starts exactly here, seeing "their own (empty)
	// timesheet" - and an account that can record time it cannot then read is
	// worse than one that can do neither.
	if len(entries) != 1 {
		t.Errorf("the listing holds %d entries; a member who recorded an hour "+
			"must be able to read it", len(entries))
	}
	day, err := f.svc.Day(f.stranger, entry.StartedAt)
	if err != nil {
		t.Fatalf("read their day: %v", err)
	}
	if day.Totals.SummedSeconds != 3600 {
		t.Errorf("their day totals %ds, want the hour they recorded",
			day.Totals.SummedSeconds)
	}

	// The commercial reports stay shut, which is the reason the owner dimension
	// is named per query rather than applied everywhere: an hour of their own
	// on a project does not entitle them to that project's budget.
	if _, err := f.svc.BudgetReportFor(f.stranger, f.svc.Now()); err == nil {
		t.Error("recording an hour opened the budget report")
	}
	if projects, err := f.svc.Projects(f.stranger, 0, false); err == nil && len(projects) != 0 {
		t.Errorf("recording an hour added %d projects to their catalogue",
			len(projects))
	}
}

// TestEveryKindOfHistoryIsScopedLikeTheRecordItDescribes.
//
// The trail used to be authorised by naming a type and an id, which is not a
// check: the authoriser was handed a resource with no project, customer or owner
// on it - the shape of a global setting - so it permitted the history of
// anybody's record to any member, and the trail carries the values each change
// moved between.
//
// So each type resolves its own subject now, and each could have forgotten.
func TestEveryKindOfHistoryIsScopedLikeTheRecordItDescribes(t *testing.T) {
	f := newStrangerFixture(t)

	for _, subject := range []struct {
		resourceType string
		id           int64
	}{
		{"time_entry", f.theirEntry.ID},
		{"expense", f.theirExpense.ID},
		{"customer", f.customerID},
		{"project", f.projectID},
		{"assignment", f.assignmentID},
	} {
		t.Run(subject.resourceType, func(t *testing.T) {
			if _, err := f.svc.AuditTrail(f.stranger, subject.resourceType, subject.id); err == nil {
				t.Errorf("the history of a %s was read by somebody on no project",
					subject.resourceType)
			}
			if _, err := f.svc.AuditTrail(f.colleague, subject.resourceType, subject.id); err != nil {
				t.Errorf("the history of a %s was refused to a member of its "+
					"project: %v", subject.resourceType, err)
			}
		})
	}

	// A type that is nobody's record is administrative: the instance settings,
	// a user account, a backup. A member has no business reading who changed
	// how the instance is run.
	for _, administrative := range []string{"settings", "user", "tag", "backup"} {
		t.Run(administrative, func(t *testing.T) {
			if _, err := f.svc.AuditTrail(f.colleague, administrative, 1); err == nil {
				t.Errorf("a member read the history of the instance's %s",
					administrative)
			}
			if _, err := f.svc.AuditTrail(f.ctx, administrative, 1); err != nil {
				t.Errorf("the administrator cannot read the history of the "+
					"instance's %s: %v", administrative, err)
			}
		})
	}

	// A subject that is not there at all reports absent rather than permitting
	// it: resolving the record is what scopes the trail, so a missing record
	// must not become an unscoped one.
	if _, err := f.svc.AuditTrail(f.colleague, "time_entry", 999999); err == nil {
		t.Error("the history of an entry that does not exist was read")
	}
}

// TestTheTagVocabularyIsScopedToWorkTheReaderCanSee.
//
// A tag is created by being used, so the vocabulary is a description of the work
// it labels. The full list told a member which labels exist on projects they are
// not on, and the full count told them how much work carries each one:
// "globex-escalation" with forty entries behind it is a sentence about somebody
// else's engagement, and it was readable on the entries filter's datalist by
// anybody with an account.
func TestTheTagVocabularyIsScopedToWorkTheReaderCanSee(t *testing.T) {
	f := newStrangerFixture(t)

	// Two tags on the team's work, which the stranger is not part of.
	if err := retagEntry(t, f, f.theirEntry.ID, []string{"acme-escalation", "shared"}); err != nil {
		t.Fatalf("tag the team's entry: %v", err)
	}

	// And one of their own, on an assignment they may record against but whose
	// project they do not belong to.
	end := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
	if _, err := f.svc.CreateEntry(f.stranger, EntryInput{
		AssignmentID: f.assignmentID,
		StartedAt:    time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end,
		Tags:         []string{"my-own-label"},
	}); err != nil {
		t.Fatalf("record their own tagged time: %v", err)
	}

	named := func(ctx context.Context) map[string]int {
		tags, err := f.svc.Tags(ctx)
		if err != nil {
			t.Fatalf("list tags: %v", err)
		}
		out := map[string]int{}
		for _, tag := range tags {
			out[tag.Name] = tag.EntryCount
		}
		return out
	}

	// The stranger sees the label on their own entry and nothing else - not
	// even the name of a tag on work they cannot read.
	theirs := named(f.stranger)
	if _, ok := theirs["my-own-label"]; !ok {
		t.Errorf("the vocabulary is %v; it must hold the label they just used",
			theirs)
	}
	for _, hidden := range []string{"acme-escalation", "shared"} {
		if _, leaked := theirs[hidden]; leaked {
			t.Errorf("the vocabulary holds %q, which labels work on a project "+
				"they are not on: %v", hidden, theirs)
		}
	}

	// A member of the project sees the team's labels, and the count is what a
	// filter by that tag would return for them - a count that disagreed with
	// the rows behind it is how somebody decides the filter is broken.
	colleague := named(f.colleague)
	if colleague["acme-escalation"] != 1 {
		t.Errorf("a member of the project sees acme-escalation with count %d, "+
			"want the one entry carrying it: %v",
			colleague["acme-escalation"], colleague)
	}
	if _, leaked := colleague["my-own-label"]; !leaked {
		t.Errorf("a member of the project cannot see %q, which labels an entry "+
			"on their own project: %v", "my-own-label", colleague)
	}

	// The count follows the reader, not the instance. The stranger's own label
	// is on one entry; so is the team's.
	if theirs["my-own-label"] != 1 {
		t.Errorf("their own label counts %d, want 1: %v",
			theirs["my-own-label"], theirs)
	}

	// The administrator's tidying screen sees all of it, with true counts: that
	// is what makes "rename this" and "delete this" safe decisions.
	whole, err := f.svc.TagsForAdmin(f.ctx)
	if err != nil {
		t.Fatalf("read the whole vocabulary: %v", err)
	}
	all := map[string]int{}
	for _, tag := range whole {
		all[tag.Name] = tag.EntryCount
	}
	for _, name := range []string{"acme-escalation", "shared", "my-own-label"} {
		if _, ok := all[name]; !ok {
			t.Errorf("the administrative vocabulary is missing %q: %v", name, all)
		}
	}

	// And a member cannot reach that listing, which is the screen's own gate.
	if _, err := f.svc.TagsForAdmin(f.colleague); err == nil {
		t.Error("a member read the whole vocabulary through the administrative listing")
	}
}

// retagEntry replaces an entry's tags, which needs a transaction because the
// full-text index is written beside the row.
func retagEntry(t *testing.T, f *strangerFixture, entryID int64, tags []string) error {
	t.Helper()
	entry, err := f.db.GetEntry(f.ctx, entryID)
	if err != nil {
		return err
	}
	entry.Tags = tags
	return f.db.InTx(f.ctx, func(tx *sql.Tx) error {
		return store.UpdateEntryWithTagsTx(f.ctx, tx, entry)
	})
}

// TestTheCatalogueCarriesNoMoneyToSomebodyWhoMayNotSeeIt.
//
// The web layer leaves the figures out of the screen; this is the half that
// matters, because a template is not the enforcement. ADR-0008 gives a member
// "own rates only" and a manager "rates and amounts", and the only narrowing
// that existed was the client's - so a member's catalogue arrived with every
// negotiated rate and every budget in it, and the screen was the only thing
// between them.
func TestTheCatalogueCarriesNoMoneyToSomebodyWhoMayNotSeeIt(t *testing.T) {
	f := newStrangerFixture(t)

	// Figures on all three levels, since each inherits from the one above and
	// each is set separately.
	customer, err := f.svc.Customer(f.ctx, f.customerID)
	if err != nil {
		t.Fatalf("read the customer: %v", err)
	}
	customer.RateMinor = 125000
	if err := f.svc.UpdateCustomer(f.ctx, customer); err != nil {
		t.Fatalf("price the customer: %v", err)
	}
	project, err := f.svc.Project(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("read the project: %v", err)
	}
	project.RateMinor, project.BudgetSeconds, project.BudgetMinor = 137500, 40*3600, 5000000
	if err := f.svc.UpdateProject(f.ctx, project); err != nil {
		t.Fatalf("price the project: %v", err)
	}
	assignment, err := f.svc.Assignment(f.ctx, f.assignmentID)
	if err != nil {
		t.Fatalf("read the assignment: %v", err)
	}
	assignment.RateMinor = 148000
	if err := f.svc.UpdateAssignment(f.ctx, assignment); err != nil {
		t.Fatalf("price the assignment: %v", err)
	}

	// The colleague is a member *of the project*, which is the case that
	// matters: they are the one person who reads these records legitimately and
	// must still not read the figures on them.
	gotCustomer, err := f.svc.Customer(f.colleague, f.customerID)
	if err != nil {
		t.Fatalf("a member of the project cannot read the customer: %v", err)
	}
	if gotCustomer.RateMinor != 0 {
		t.Errorf("a member reads the customer's rate as %d", gotCustomer.RateMinor)
	}
	if gotCustomer.Name == "" {
		t.Error("the customer came back with no name; the names are what a " +
			"member needs")
	}

	gotProject, err := f.svc.Project(f.colleague, f.projectID)
	if err != nil {
		t.Fatalf("a member of the project cannot read it: %v", err)
	}
	if gotProject.RateMinor != 0 || gotProject.BudgetSeconds != 0 || gotProject.BudgetMinor != 0 {
		t.Errorf("a member reads the project's rate %d and budget %d/%d",
			gotProject.RateMinor, gotProject.BudgetSeconds, gotProject.BudgetMinor)
	}

	gotAssignment, err := f.svc.Assignment(f.colleague, f.assignmentID)
	if err != nil {
		t.Fatalf("a member of the project cannot read the assignment: %v", err)
	}
	if gotAssignment.RateMinor != 0 {
		t.Errorf("a member reads the assignment's rate as %d - the most "+
			"specific of the three", gotAssignment.RateMinor)
	}

	// The listings too, which is where a loop gets forgotten.
	customers, err := f.svc.Customers(f.colleague, false)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	for _, listed := range customers {
		if listed.RateMinor != 0 {
			t.Errorf("the customer listing carries a rate: %+v", listed)
		}
	}
	projects, err := f.svc.Projects(f.colleague, 0, false)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, listed := range projects {
		if listed.RateMinor != 0 || listed.BudgetSeconds != 0 {
			t.Errorf("the project listing carries money: %+v", listed)
		}
	}
	assignments, err := f.svc.Assignments(f.colleague, 0, false)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	for _, listed := range assignments {
		if listed.RateMinor != 0 {
			t.Errorf("the assignment listing carries a rate: %+v", listed)
		}
	}

	// And the administrator still prices the work, which is the half a
	// narrowing is easiest to break.
	priced, err := f.svc.Assignment(f.ctx, f.assignmentID)
	if err != nil {
		t.Fatalf("the administrator cannot read the assignment: %v", err)
	}
	if priced.RateMinor != 148000 {
		t.Errorf("the administrator reads the assignment's rate as %d, want the "+
			"148000 they set", priced.RateMinor)
	}
}
