package service

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// What the service does when the database underneath it is gone.
//
// Every method here ends by returning what it read, or the error it hit. The
// handlers above trust that: a read that returned an empty slice and a nil
// error would put "no entries this week" on a screen, and the person reading it
// would believe it - a quiet empty page and a genuinely empty week look the
// same. Worse, a write that swallowed its error would report success for a
// change the database never made.
//
// So this closes the database and calls every method, requiring an error from
// each. It is one test for the whole surface on purpose: the alternative is a
// hundred tests that each assert the same two lines, and the thing worth
// catching is a method added later that forgets them.

// deadFixture is the usual fixture with its database closed from under it,
// after everything a method might need an id for has been created.
type deadFixture struct {
	*fixture
	entryID, expenseID, routineID, tagID, termsID, attachmentID, idleID int64
	customerID, projectID                                               int64
	backupDir, backupName                                               string
}

func newDeadFixture(t *testing.T) *deadFixture {
	t.Helper()
	f := newFixture(t)
	dead := &deadFixture{fixture: f, backupDir: t.TempDir()}

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	dead.projectID = project.ID
	dead.customerID = project.CustomerID

	// A whole working day, with the clock set after it, so an idle observation
	// inside it is a question somebody still has to answer.
	f.now = at(f.now, 16, 0)
	end := at(f.now, 15, 0)
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0), EndedAt: &end,
		Billable: true, Tags: []string{"incident"},
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	dead.entryID = entry.ID

	expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi", Amount: "100", Billable: true,
	})
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}
	dead.expenseID = expense.ID

	if err := f.svc.SaveRoutine(f.ctx, domain.Routine{
		AssignmentID: f.assignment.ID, Name: "Standup", Weekdays: []int{1},
		DurationSeconds: 900, Active: true,
	}); err != nil {
		t.Fatalf("save routine: %v", err)
	}
	routines, err := f.svc.Routines(f.ctx, false)
	if err != nil || len(routines) == 0 {
		t.Fatalf("read routines: %v", err)
	}
	dead.routineID = routines[0].ID

	tags, err := f.svc.Tags(f.ctx)
	if err != nil || len(tags) == 0 {
		t.Fatalf("read tags: %v", err)
	}
	dead.tagID = tags[0].ID

	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2026-01-01", Rules: domain.RateRules{ExpenseMarkupPct: 10},
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}
	view, err := f.svc.ContractTerms(f.ctx, domain.TermsForCustomer, project.CustomerID)
	if err != nil || len(view.Terms) == 0 {
		t.Fatalf("read terms: %v", err)
	}
	dead.termsID = view.Terms[0].ID

	// An attachment needs a blob store, which the plain fixture has none of.
	withBlobs(t, f)
	attachment, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry, entry.ID,
		"receipt.txt", strings.NewReader("a receipt"))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	dead.attachmentID = attachment.ID

	if _, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 12, 0), at(f.now, 13, 0), domain.IdleUntouched); err != nil {
		t.Fatalf("record idle: %v", err)
	}
	pending, err := f.svc.PendingIdle(f.ctx)
	if err != nil || len(pending) == 0 {
		t.Fatalf("read the open idle observation: %v", err)
	}
	dead.idleID = pending[0].Observation.ID

	// A real archive in the backup directory, so a preview of it fails for want
	// of a database rather than for want of a file.
	dead.backupName = "timetracker-20260316-090000.zip"
	if err := os.WriteFile(filepath.Join(dead.backupDir, dead.backupName),
		buildArchive(t), 0o600); err != nil {
		t.Fatalf("plant a backup: %v", err)
	}

	if err := f.db.Close(); err != nil {
		t.Fatalf("close the database: %v", err)
	}
	return dead
}

// TestEveryServiceMethodReportsADeadDatabase.
//
// See the note at the top of this file.
func TestEveryServiceMethodReportsADeadDatabase(t *testing.T) {
	f := newDeadFixture(t)
	svc, ctx := f.svc, f.ctx

	// A small archive, so the restore paths get past reading their input and
	// fail on the database rather than on the file.
	archive := buildArchive(t)

	calls := map[string]func() error{
		"AcceptEntry":       func() error { _, err := svc.AcceptEntry(ctx, f.entryID); return err },
		"AcceptExpense":     func() error { return svc.AcceptExpense(ctx, f.expenseID) },
		"ApplyAllRoutines":  func() error { _, err := svc.ApplyAllRoutines(ctx, f.now); return err },
		"ApplyRoutine":      func() error { _, err := svc.ApplyRoutine(ctx, f.routineID, f.now); return err },
		"ApprovalReportFor": func() error { _, err := svc.ApprovalReportFor(ctx, f.now, 4); return err },
		"ApproveWeek":       func() error { return svc.ApproveWeek(ctx, f.user.ID, "2026-03-16") },
		"Assignment":        func() error { _, err := svc.Assignment(ctx, f.assignment.ID); return err },
		"Assignments":       func() error { _, err := svc.Assignments(ctx, 0, false); return err },
		"Attach": func() error {
			_, err := svc.Attach(ctx, domain.AttachmentOwnerTimeEntry, f.entryID, "x.txt", strings.NewReader("x"))
			return err
		},
		"Attachments":     func() error { _, err := svc.Attachments(ctx, domain.AttachmentOwnerTimeEntry, f.entryID); return err },
		"AuditTrail":      func() error { _, err := svc.AuditTrail(ctx, "time_entry", f.entryID); return err },
		"BudgetReportFor": func() error { _, err := svc.BudgetReportFor(ctx, f.now); return err },
		"ContractTerms":   func() error { _, err := svc.ContractTerms(ctx, domain.TermsForCustomer, f.customerID); return err },
		"CopyDay":         func() error { _, err := svc.CopyDay(ctx, f.now.AddDate(0, 0, -1), f.now); return err },
		"CopyWeek":        func() error { _, err := svc.CopyWeek(ctx, f.now.AddDate(0, 0, -7), f.now); return err },
		"CountEntries":    func() error { _, err := svc.CountEntries(ctx, EntryFilter{}); return err },
		"CreateAssignment": func() error {
			_, err := svc.CreateAssignment(ctx, domain.Assignment{ProjectID: f.projectID, Name: "New"})
			return err
		},
		"CreateBackup": func() error { _, err := svc.CreateBackup(ctx, BackupOptions{}); return err },
		"CreateCustomer": func() error {
			_, err := svc.CreateCustomer(ctx, domain.Customer{Name: "New", Currency: "EUR"})
			return err
		},
		"CreateEntry": func() error {
			_, err := svc.CreateEntry(ctx, EntryInput{AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 3600})
			return err
		},
		"CreateExpense": func() error {
			_, err := svc.CreateExpense(ctx, ExpenseInput{ProjectID: f.projectID, SpentOn: "2026-03-16", Category: "travel", Amount: "10"})
			return err
		},
		"CreateProject": func() error {
			_, err := svc.CreateProject(ctx, domain.Project{CustomerID: f.customerID, Name: "New"})
			return err
		},
		"Customer":            func() error { _, err := svc.Customer(ctx, f.customerID); return err },
		"Customers":           func() error { _, err := svc.Customers(ctx, false); return err },
		"Day":                 func() error { _, err := svc.Day(ctx, f.now); return err },
		"DeleteAttachment":    func() error { return svc.DeleteAttachment(ctx, f.attachmentID) },
		"DeleteContractTerms": func() error { return svc.DeleteContractTerms(ctx, f.termsID) },
		"DeleteEntry":         func() error { return svc.DeleteEntry(ctx, f.entryID) },
		"DeleteExpense":       func() error { return svc.DeleteExpense(ctx, f.expenseID) },
		"DeleteRoutine":       func() error { return svc.DeleteRoutine(ctx, f.routineID) },
		"DeleteTag":           func() error { return svc.DeleteTag(ctx, f.tagID) },
		"DismissReminder":     func() error { return svc.DismissReminder(ctx, domain.ReminderEmptyDay, "2026-03-16") },
		"EachEntry": func() error {
			for _, err := range svc.EachEntry(ctx, EntryFilter{}) {
				if err != nil {
					return err
				}
			}
			return nil
		},
		"EnsureLocalUser":      func() error { return svc.EnsureLocalUser(ctx, "Someone", "UTC") },
		"Entries":              func() error { _, err := svc.Entries(ctx, EntryFilter{}); return err },
		"Entry":                func() error { _, err := svc.Entry(ctx, f.entryID); return err },
		"Expense":              func() error { _, err := svc.Expense(ctx, f.expenseID); return err },
		"Expenses":             func() error { _, err := svc.Expenses(ctx, ExpenseFilter{}); return err },
		"IdleThresholdSeconds": func() error { _, err := svc.IdleThresholdSeconds(ctx); return err },
		"ImportCalendar": func() error {
			_, err := svc.ImportCalendar(ctx, strings.NewReader(oneEventCalendar), map[string]int64{"one": f.assignment.ID}, "")
			return err
		},
		"ImportTimeCSV": func() error { _, err := svc.ImportTimeCSV(ctx, strings.NewReader(oneRowCSV), false); return err },
		"Inbox":         func() error { _, err := svc.Inbox(ctx); return err },
		"LocalUser":     func() error { _, err := svc.LocalUser(ctx); return err },
		"Month":         func() error { _, err := svc.Month(ctx, f.now); return err },
		"MoveEntries":   func() error { _, err := svc.MoveEntries(ctx, []int64{f.entryID}, f.assignment.ID); return err },
		"MoveExpenses":  func() error { _, err := svc.MoveExpenses(ctx, []int64{f.expenseID}, f.projectID); return err },
		"NeedsReceipt": func() error {
			_, err := svc.NeedsReceipt(ctx, domain.Expense{ProjectID: f.projectID, AmountMinor: 100000})
			return err
		},
		"OpenAttachment":  func() error { _, _, err := svc.OpenAttachment(ctx, f.attachmentID); return err },
		"OvertimeNotices": func() error { _, err := svc.OvertimeNotices(ctx, f.now); return err },
		"ParseCalendar": func() error {
			_, err := svc.ParseCalendar(ctx, strings.NewReader(oneEventCalendar), f.assignment.ID)
			return err
		},
		"ParseQuickAdd":     func() error { _, err := svc.ParseQuickAdd(ctx, "2h Development"); return err },
		"ParseTimeCSV":      func() error { _, err := svc.ParseTimeCSV(ctx, strings.NewReader(oneRowCSV), false); return err },
		"PendingCount":      func() error { _, err := svc.PendingCount(ctx); return err },
		"PendingIdle":       func() error { _, err := svc.PendingIdle(ctx); return err },
		"Period":            func() error { _, err := svc.Period(ctx, f.now); return err },
		"PreviewExport":     func() error { _, err := svc.PreviewExport(ctx, EntryFilter{}); return err },
		"PreviewAttachment": func() error { _, _, err := svc.PreviewAttachment(ctx, f.attachmentID); return err },
		"Project":           func() error { _, err := svc.Project(ctx, f.projectID); return err },
		"Projects":          func() error { _, err := svc.Projects(ctx, 0, false); return err },
		"ProposedByMe":      func() error { _, err := svc.ProposedByMe(ctx); return err },
		"QuickAdd":          func() error { _, _, err := svc.QuickAdd(ctx, "2h Development"); return err },
		"QuickStart":        func() error { _, err := svc.QuickStart(ctx, 10); return err },
		"RecentAssignments": func() error { _, err := svc.RecentAssignments(ctx, 10); return err },
		"RecentPeriods":     func() error { _, err := svc.RecentPeriods(ctx); return err },
		"RecordIdle": func() error {
			_, err := svc.RecordIdle(ctx, f.entryID, f.now, f.now.Add(time.Minute), domain.IdleUntouched)
			return err
		},
		"ReindexSearch":    func() error { _, err := svc.ReindexSearch(ctx); return err },
		"RejectEntry":      func() error { return svc.RejectEntry(ctx, f.entryID, "no") },
		"RejectExpense":    func() error { return svc.RejectExpense(ctx, f.expenseID, "no") },
		"ReviewSubmission": func() error { _, err := svc.ReviewSubmission(ctx, f.now); return err },
		"RejectWeek":       func() error { return svc.RejectWeek(ctx, f.user.ID, "2026-03-16", "no") },
		"Reminders":        func() error { _, err := svc.Reminders(ctx); return err },
		"ReopenWeek":       func() error { return svc.ReopenWeek(ctx, f.user.ID, "2026-03-16", "no") },
		"ResolveIdle":      func() error { return svc.ResolveIdle(ctx, f.idleID, domain.IdleKeep) },
		"Restore":          func() error { _, err := svc.Restore(ctx, bytes.NewReader(archive)); return err },
		"RestoreArchive": func() error {
			_, err := svc.RestoreArchive(ctx, bytes.NewReader(archive), int64(len(archive)))
			return err
		},
		// Both are given a real, readable archive, so a nil error here would
		// mean the authorisation check they begin with went unasked rather than
		// that the file was unreadable.
		"PreviewArchive": func() error {
			_, err := svc.PreviewArchive(ctx, bytes.NewReader(archive), int64(len(archive)))
			return err
		},
		"PreviewBackupFile": func() error {
			_, err := svc.PreviewBackupFile(ctx, f.backupDir, f.backupName)
			return err
		},
		"Routines":      func() error { _, err := svc.Routines(ctx, false); return err },
		"RoutinesDue":   func() error { _, err := svc.RoutinesDue(ctx, f.now); return err },
		"RunningIdle":   func() error { _, err := svc.RunningIdle(ctx); return err },
		"RunningTimers": func() error { _, err := svc.RunningTimers(ctx); return err },
		"SaveContractTerms": func() error {
			return svc.SaveContractTerms(ctx, domain.ContractTerms{Scope: domain.TermsForCustomer, ScopeID: f.customerID, EffectiveFrom: "2026-02-01"})
		},
		"SaveRoutine": func() error {
			return svc.SaveRoutine(ctx, domain.Routine{AssignmentID: f.assignment.ID, Name: "Another", Weekdays: []int{2}, DurationSeconds: 900})
		},
		"SearchEntries":         func() error { _, _, err := svc.SearchEntries(ctx, EntryFilter{Query: "anything"}); return err },
		"SearchEntriesPage":     func() error { _, err := svc.SearchEntriesPage(ctx, EntryFilter{Query: "anything"}); return err },
		"SetAssignmentArchived": func() error { return svc.SetAssignmentArchived(ctx, f.assignment.ID, true) },
		"SetCustomerArchived":   func() error { return svc.SetCustomerArchived(ctx, f.customerID, true) },
		"SetFavourite":          func() error { return svc.SetFavourite(ctx, f.assignment.ID, true) },
		"SetLanguage":           func() error { return svc.SetLanguage(ctx, "sv") },
		"SetProjectArchived":    func() error { return svc.SetProjectArchived(ctx, f.projectID, true) },
		"SetTheme":              func() error { return svc.SetTheme(ctx, "dark") },
		"SetTimeZone":           func() error { return svc.SetTimeZone(ctx, "Europe/Stockholm") },
		"Settings":              func() error { _, err := svc.Settings(ctx); return err },
		"StartTimer":            func() error { _, err := svc.StartTimer(ctx, f.assignment.ID, ""); return err },
		"StopAllTimers":         func() error { _, err := svc.StopAllTimers(ctx); return err },
		"StopTimer":             func() error { _, err := svc.StopTimer(ctx, f.entryID); return err },
		"SubmitWeek":            func() error { _, err := svc.SubmitWeek(ctx, f.now); return err },
		"SweepBlobs":            func() error { _, err := svc.SweepBlobs(ctx); return err },
		"SwitchTo":              func() error { _, err := svc.SwitchTo(ctx, f.assignment.ID, ""); return err },
		"Tags":                  func() error { _, err := svc.Tags(ctx); return err },
		"TermsCurrency":         func() error { _, err := svc.TermsCurrency(ctx, domain.TermsForCustomer, f.customerID); return err },
		"TermsOn":               func() error { _, err := svc.TermsOn(ctx, f.customerID, f.projectID, f.now); return err },
		"UpdateAssignment":      func() error { return svc.UpdateAssignment(ctx, f.assignment) },
		"UpdateCustomer": func() error {
			return svc.UpdateCustomer(ctx, domain.Customer{ID: f.customerID, Name: "Acme", Currency: "SEK"})
		},
		"UpdateEntry": func() error {
			_, err := svc.UpdateEntry(ctx, f.entryID, EntryInput{AssignmentID: f.assignment.ID, StartedAt: f.now, DurationSeconds: 3600})
			return err
		},
		"UpdateExpense": func() error {
			_, err := svc.UpdateExpense(ctx, f.expenseID, ExpenseInput{ProjectID: f.projectID, SpentOn: "2026-03-16", Category: "travel", Amount: "10"})
			return err
		},
		"UpdateProject": func() error {
			return svc.UpdateProject(ctx, domain.Project{ID: f.projectID, CustomerID: f.customerID, Name: "Migration"})
		},
		"UpdateSettings":  func() error { return svc.UpdateSettings(ctx, SettingsInput{DefaultCurrency: "SEK", WeekStart: 1}) },
		"UpdateTag":       func() error { return svc.UpdateTag(ctx, domain.Tag{ID: f.tagID, Name: "outage"}) },
		"Week":            func() error { _, err := svc.Week(ctx, f.now); return err },
		"WithdrawWeek":    func() error { return svc.WithdrawWeek(ctx, f.now) },
		"WriteArchive":    func() error { return svc.WriteArchive(ctx, io.Discard, BackupOptions{}) },
		"WriteBackup":     func() error { return svc.WriteBackup(ctx, io.Discard, BackupOptions{}) },
		"WriteBackupFile": func() error { _, err := svc.WriteBackupFile(ctx, f.backupDir, BackupOptions{}); return err },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Errorf("%s reported success against a closed database - a read "+
					"that answers \"nothing\" and a write that answers \"done\" are "+
					"both lies the layer above has no way to see through", name)
			}
		})
	}

	// TextPreviews answers with what it could read rather than an error,
	// because a preview is a convenience on a screen that has already loaded.
	// Asserted here so that choice is visible beside the others rather than
	// looking like an omission.
	if previews := svc.TextPreviews(ctx, []domain.Attachment{
		{ID: f.attachmentID, MIME: "text/plain", SizeBytes: 9},
	}); len(previews) != 0 {
		t.Errorf("TextPreviews returned %d previews from a closed database", len(previews))
	}

	// AuthorizeBackup asks the authoriser rather than the database, so it is
	// deliberately unaffected.
	if err := svc.AuthorizeBackup(ctx); err != nil {
		t.Errorf("AuthorizeBackup = %v; it asks the authoriser, not the database", err)
	}

	// The two approval queues never reach the database here, and that is not a
	// gap in the sweep: local mode refuses approval outright - there is one
	// person, and approving your own timesheet is not approval - and both
	// methods answer a refusal with an empty queue rather than an error, so the
	// screen says "nothing to approve" instead of failing to load.
	//
	// Asserted rather than skipped, because an empty queue is the right answer
	// only while the refusal is the reason for it.
	for name, call := range map[string]func() ([]domain.TimesheetPeriod, error){
		"PendingApprovals": func() ([]domain.TimesheetPeriod, error) { return svc.PendingApprovals(ctx) },
		"ApprovedPeriods":  func() ([]domain.TimesheetPeriod, error) { return svc.ApprovedPeriods(ctx) },
	} {
		queue, err := call()
		if err != nil || len(queue) != 0 {
			t.Errorf("%s = %v, %v; in local mode approval is refused and the "+
				"queue is empty rather than an error", name, queue, err)
		}
	}

	// The table is only as good as its completeness, and a list in a test file
	// stops being complete the week after it is written. Reflect over the type
	// and require every method that does database work - which is to say every
	// one taking a context - to be in it, so one added later fails this rather
	// than quietly reducing what is checked.
	exempt := map[string]bool{
		"TextPreviews": true, "AuthorizeBackup": true,
		"PendingApprovals": true, "ApprovedPeriods": true,
	}
	serviceType := reflect.TypeOf(svc)
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	checked := 0
	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		if method.Type.NumIn() < 2 || method.Type.In(1) != ctxType {
			continue
		}
		checked++
		if calls[method.Name] == nil && !exempt[method.Name] {
			t.Errorf("%s takes a context and is not in this test's table, so "+
				"nothing checks that it reports a dead database. Add it, or add "+
				"it to the exempt list with the reason.", method.Name)
		}
	}
	if checked == 0 {
		t.Error("reflection found no context-taking methods, so this check is reading nothing")
	}
}

// oneEventCalendar and oneRowCSV are the smallest valid inputs, so a method
// that parses before it reads gets as far as the database.
const oneEventCalendar = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:one\r\nDTSTART:20260316T090000Z\r\nDTEND:20260316T100000Z\r\n" +
	"SUMMARY:Planning\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

const oneRowCSV = "date,hours,assignment\n2026-03-16,1.5,Development\n"

// buildArchive writes a backup from a live instance, for the restore paths.
func buildArchive(t *testing.T) []byte {
	t.Helper()
	f := newFixture(t)
	var buf bytes.Buffer
	if err := f.svc.WriteArchive(f.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return buf.Bytes()
}
