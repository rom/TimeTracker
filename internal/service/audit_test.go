package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// ASR-006, the half that cannot be tested by observing a successful mutation.
//
// TestAuditTrailRecordsEveryMutation proves a change leaves a row. It does not
// prove the two are atomic, and it cannot: a service that wrote the audit row in
// its own separate transaction would pass it exactly as well. Atomicity is only
// visible when something fails, so these break one of the two writes on purpose
// and check what survives.
//
// Both directions matter, and they fail differently:
//
//   - a change that commits without its audit row is a change nobody can
//     account for afterwards. The record exists, and the trail says it never
//     happened.
//   - an audit row that survives a rolled-back change is worse, because it is
//     not silent - it is a confident claim that somebody did something they did
//     not do, and it is the kind of thing that gets cited.
//
// The failure is injected in SQLite rather than in Go, with a trigger that
// aborts the insert. That is deliberate: a fake store would prove the service
// calls what we think it calls, and what is under test here is whether the
// database actually rolls the other statement back.

// breakInserts installs a trigger that aborts every insert into a table.
//
// RAISE(ABORT) rolls back the statement and returns an error, which is exactly
// what a constraint violation, a disk error or a lock timeout looks like from
// Go: the transaction is still open and the caller decides. That makes this a
// realistic injection rather than a special case.
func breakInserts(t *testing.T, db *store.DB, table string) {
	t.Helper()

	err := db.InTx(context.Background(), func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(context.Background(),
			`CREATE TRIGGER break_`+table+` BEFORE INSERT ON `+table+`
			 BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
		return execErr
	})
	if err != nil {
		t.Fatalf("install the failure on %s: %v", table, err)
	}
	t.Cleanup(func() {
		_ = db.InTx(context.Background(), func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS break_`+table)
			return execErr
		})
	})
}

// countRows counts a table directly, without going through anything that could
// itself be filtering.
func countRows(t *testing.T, db *store.DB, table, where string, args ...any) int {
	t.Helper()

	query := `SELECT COUNT(*) FROM ` + table
	if where != "" {
		query += ` WHERE ` + where
	}
	var count int
	err := db.InTx(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&count)
	})
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// TestAFailedAuditRollsBackTheChange.
//
// The audit write is the last statement in the transaction, which is the
// dangerous position: everything the user asked for has already succeeded, and
// the temptation - in a rewrite, or under a deadline - is to log the audit
// failure and commit anyway, because the user's work is right there and throwing
// it away feels wrong.
//
// It is not wrong. A change that cannot be accounted for is precisely what an
// audit requirement exists to prevent, and a system that keeps the change and
// drops the record has quietly become one that cannot answer "who did this".
func TestAFailedAuditRollsBackTheChange(t *testing.T) {
	f := newFixture(t)
	before := countRows(t, f.db, "time_entries", "")

	breakInserts(t, f.db, "audit_events")

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "should not survive"); err == nil {
		t.Fatal("StartTimer succeeded with the audit write failing; the change was " +
			"committed without a record of who made it")
	} else if !strings.Contains(err.Error(), "injected failure") {
		// The error has to be the injected one. If some earlier validation
		// refused the call, this test would pass while proving nothing.
		t.Fatalf("StartTimer failed for the wrong reason: %v", err)
	}

	if after := countRows(t, f.db, "time_entries", ""); after != before {
		t.Errorf("the entry survived a failed audit write: %d entries before, %d after",
			before, after)
	}
	if running, err := f.svc.RunningTimers(f.ctx); err != nil {
		t.Fatalf("running timers: %v", err)
	} else if len(running) != 0 {
		t.Errorf("a timer is running after the transaction that started it rolled back")
	}
}

// auditNotAtomic names the mutations that still write their audit row in a
// *second* transaction, and are therefore not atomic with the change.
//
// This is not an exemption in the sense the other lists in this repository are.
// Those name things that are safe for a stated reason; every entry here is a
// real gap, found by the test below.
//
// The shape is always the same: the store writes the record on its own
// connection, and the service then calls recordAudit, which opens a transaction
// of its own. If the audit insert fails, the caller is told the operation
// failed - and the record is there anyway. A user who retries gets two of
// whatever they were creating, and the trail has no row for either.
//
// The list is empty. It stays here because the test checks it in both
// directions: a path named here that turns out to be atomic fails too, so
// whoever fixes one is told to delete the line rather than leaving a list of
// defects that quietly includes fixed ones. What remains un-atomic elsewhere in
// the service - the importers, the restore, an attachment whose bytes are
// already on disk - is a change that is not a single database write, and each
// needs its own answer rather than this one.
var auditNotAtomic = map[string]string{}

// TestEveryAuditedMutationRollsBackTogether.
//
// Atomicity is a property of each transaction rather than of the codebase, so it
// has to be asked of each path: one method that commits the change before
// writing the audit row leaves every other test green.
//
// Asking it of several at once is what turned up the catalogue: the entry paths,
// which were the ones that had ever been tested, were correct, and none of the
// catalogue paths were - they wrote the record on one connection and the audit
// row on another. They go through Service.mutate now, which is the same
// arrangement the entry paths use.
func TestEveryAuditedMutationRollsBackTogether(t *testing.T) {
	for _, mutation := range []struct {
		name  string
		table string
		// setup runs before the failure is injected, for a case that needs
		// something to exist first - an entry to attach a receipt to. It cannot
		// be part of do, because by then every audit write fails and the setup
		// would be the thing that failed.
		setup func(t *testing.T, f *fixture)
		do    func(t *testing.T, f *fixture) error
	}{
		{
			name:  "start a timer",
			table: "time_entries",
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
				return err
			},
		},
		{
			name:  "record an entry",
			table: "time_entries",
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.CreateEntry(f.ctx, EntryInput{
					AssignmentID: f.assignment.ID, StartedAt: f.now,
					DurationSeconds: 3600, Billable: true,
				})
				return err
			},
		},
		{
			name:  "add a customer",
			table: "customers",
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
					Name: "Rolled Back", Currency: "SEK",
				})
				return err
			},
		},
		{
			name:  "add a project",
			table: "projects",
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.CreateProject(f.ctx, domain.Project{
					CustomerID: 1, Name: "Rolled Back", BillableDefault: true,
				})
				return err
			},
		},
		{
			name:  "rename a customer",
			table: "customers",
			do: func(t *testing.T, f *fixture) error {
				return f.svc.UpdateCustomer(f.ctx, domain.Customer{
					ID: 1, Name: "Renamed", Currency: "SEK",
				})
			},
		},
		{
			name:  "add an assignment",
			table: "assignments",
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
					ProjectID: 1, Name: "Rolled Back", BillableDefault: true,
				})
				return err
			},
		},
		{
			name:  "archive a customer",
			table: "customers",
			do: func(t *testing.T, f *fixture) error {
				return f.svc.SetCustomerArchived(f.ctx, 1, true)
			},
		},
		{
			name:  "archive a project",
			table: "projects",
			do: func(t *testing.T, f *fixture) error {
				return f.svc.SetProjectArchived(f.ctx, 1, true)
			},
		},
		{
			// Every row of the file, or none of it. The summary row saying how
			// many were imported is written in the same transaction as the rows
			// it counts, which is the only way it can be true.
			name:  "import a CSV of hours",
			table: "time_entries",
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(
					"date,assignment,hours\n2026-03-16,Development,1.5\n2026-03-17,Development,2\n"), false)
				return err
			},
		},
		{
			name:  "import a calendar",
			table: "time_entries",
			do: func(t *testing.T, f *fixture) error {
				file := calendarFile(f.now, vevent("x", "A meeting", "080000", "090000", ""))
				_, err := f.svc.ImportCalendar(f.ctx, strings.NewReader(file),
					map[string]int64{"x": f.assignment.ID}, "")
				return err
			},
		},
		{
			// The bytes are deliberately not in the transaction: they are
			// content-addressed and written first, so a rollback leaves a file
			// nothing references rather than a row pointing at nothing.
			name:  "attach a receipt",
			table: "attachments",
			setup: func(t *testing.T, f *fixture) {
				withBlobs(t, f)
				mustCreate(t, f, f.now, 3600)
			},
			do: func(t *testing.T, f *fixture) error {
				_, err := f.svc.Attach(f.ctx, "time_entry", 1,
					"receipt.txt", strings.NewReader("bytes"))
				return err
			},
		},
		{
			name:  "delete a receipt",
			table: "attachments",
			setup: func(t *testing.T, f *fixture) {
				withBlobs(t, f)
				entry := mustCreate(t, f, f.now, 3600)
				if _, err := f.svc.Attach(f.ctx, "time_entry", entry.ID,
					"receipt.txt", strings.NewReader("bytes")); err != nil {
					t.Fatalf("attach: %v", err)
				}
			},
			do: func(t *testing.T, f *fixture) error {
				return f.svc.DeleteAttachment(f.ctx, 1)
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newFixture(t)
			if mutation.setup != nil {
				mutation.setup(t, f)
			}
			before := countRows(t, f.db, mutation.table, "")

			breakInserts(t, f.db, "audit_events")

			err := mutation.do(t, f)
			if err == nil {
				t.Fatalf("%s succeeded with the audit write failing", mutation.name)
			}
			if !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("%s failed for the wrong reason: %v", mutation.name, err)
			}

			after := countRows(t, f.db, mutation.table, "")
			// A rename and an archive change a row rather than adding one, so
			// the count is not the evidence: the value is.
			rolledBack := after == before
			switch mutation.name {
			case "delete a receipt":
				// A delete that rolled back leaves the row where it was.
				rolledBack = after == before
			case "rename a customer":
				rolledBack = customerName(t, f, 1) != "Renamed"
			case "archive a customer":
				rolledBack = countRows(t, f.db, "customers", "archived_at IS NOT NULL") == 0
			case "archive a project":
				rolledBack = countRows(t, f.db, "projects", "archived_at IS NOT NULL") == 0
			}

			if reason, known := auditNotAtomic[mutation.name]; known {
				if rolledBack {
					t.Errorf("%s is atomic now (%s). Delete it from auditNotAtomic: "+
						"a list of known defects that includes fixed ones stops "+
						"being read.", mutation.name, reason)
				}
				return
			}
			if !rolledBack {
				t.Errorf("%s survived a failed audit write: %d rows before, %d after.\n"+
					"A change that cannot be accounted for is what ASR-006 exists to "+
					"prevent; keeping the change and dropping the record is the one "+
					"outcome the requirement rules out.",
					mutation.name, before, after)
			}
		})
	}
}

// customerName reads one customer's name directly.
func customerName(t *testing.T, f *fixture, id int64) string {
	t.Helper()

	var name string
	err := f.db.InTx(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(),
			`SELECT name FROM customers WHERE id = ?`, id).Scan(&name)
	})
	if err != nil {
		t.Fatalf("read customer %d: %v", id, err)
	}
	return name
}

// TestAFailedChangeLeavesNoAuditRow.
//
// The converse, and the one that catches an audit written in its own
// transaction. Here the *change* is what fails, and the question is whether the
// row claiming it happened was already committed somewhere else.
//
// An audit trail that overstates is not a lesser problem than one that
// understates. It is the one that gets read out in a dispute.
func TestAFailedChangeLeavesNoAuditRow(t *testing.T) {
	f := newFixture(t)
	before := countRows(t, f.db, "audit_events", "")

	breakInserts(t, f.db, "time_entries")

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, ""); err == nil {
		t.Fatal("StartTimer succeeded with the entry insert failing")
	} else if !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("StartTimer failed for the wrong reason: %v", err)
	}

	if after := countRows(t, f.db, "audit_events", ""); after != before {
		t.Errorf("%d audit rows were written for a change that never happened",
			after-before)
	}
	if rows := countRows(t, f.db, "audit_events", "action = ?", "time_entry.start"); rows != 0 {
		t.Errorf("the trail claims a timer was started %d time(s); none was", rows)
	}
}

// TestTheInjectionItselfWorks.
//
// The tests above all pass if the trigger silently does nothing: the mutation
// succeeds, `err == nil`, and... they would fail. So they cannot pass vacuously,
// and this is not guarding them against that.
//
// It guards them against the opposite: an injection so broad that the fixture is
// broken before the interesting part runs. Removing the trigger has to restore
// ordinary behaviour, which proves the failure was the audit write and not the
// database being wedged by the previous statement's rollback.
func TestTheInjectionItselfWorks(t *testing.T) {
	f := newFixture(t)

	breakInserts(t, f.db, "audit_events")
	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, ""); err == nil {
		t.Fatal("the injected trigger did not break anything")
	}

	// Dropped here rather than left to the cleanup, because the second half of
	// this test is what happens after it is gone.
	if err := f.db.InTx(context.Background(), func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS break_audit_events`)
		return execErr
	}); err != nil {
		t.Fatalf("remove the injected failure: %v", err)
	}

	entry, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "this one should stick")
	if err != nil {
		t.Fatalf("the database is still broken after removing the trigger: %v", err)
	}
	events, err := f.svc.AuditTrail(f.ctx, "time_entry", entry.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	if len(events) == 0 {
		t.Error("the ordinary path stopped writing audit rows")
	}
}

// TestNothingUpdatesOrDeletesAnAuditRow.
//
// The source scan in internal/repocheck proves no statement in the tree rewrites
// the trail. This proves the same thing about the running system, from the other
// end: after a full cycle of create, correct and delete, every row that was ever
// written is still there, unchanged.
//
// Deleting an entry is the interesting case. The record goes; the trail of what
// was done to it must not, or "deleted" becomes a way to erase history.
func TestNothingUpdatesOrDeletesAnAuditRow(t *testing.T) {
	f := newFixture(t)

	entry := mustCreate(t, f, f.now, 3600)
	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 7200, Note: "corrected", Billable: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	trail, err := f.svc.AuditTrail(f.ctx, "time_entry", entry.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	if len(trail) < 2 {
		t.Fatalf("expected the create and the correction to be recorded, got %d", len(trail))
	}

	if err := f.svc.DeleteEntry(f.ctx, entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	after, err := f.svc.AuditTrail(f.ctx, "time_entry", entry.ID)
	if err != nil {
		t.Fatalf("audit trail after the delete: %v", err)
	}
	if len(after) < len(trail)+1 {
		t.Errorf("deleting the entry lost its history: %d rows before, %d after",
			len(trail), len(after))
	}

	// The earlier rows are unchanged, not merely present: same action, same
	// actor, same instant.
	for i, event := range trail {
		if after[len(after)-len(trail)+i].Action != event.Action {
			t.Errorf("audit row %d was rewritten: %q became %q", i, event.Action,
				after[len(after)-len(trail)+i].Action)
		}
	}

	// And the deletion itself is on the record.
	var deleted bool
	for _, event := range after {
		if strings.HasSuffix(event.Action, ".delete") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("deleting an entry left no audit row")
	}
}

// breakInsertsWhen installs a trigger that aborts inserts into a table only for
// the rows matching a condition, so a failure can be injected part-way through a
// batch rather than at the start of it.
func breakInsertsWhen(t *testing.T, db *store.DB, table, when string) {
	t.Helper()

	err := db.InTx(context.Background(), func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(context.Background(),
			`CREATE TRIGGER break_`+table+`_when BEFORE INSERT ON `+table+`
			 FOR EACH ROW WHEN `+when+`
			 BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
		return execErr
	})
	if err != nil {
		t.Fatalf("install the conditional failure on %s: %v", table, err)
	}
	t.Cleanup(func() {
		_ = db.InTx(context.Background(), func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(context.Background(),
				`DROP TRIGGER IF EXISTS break_`+table+`_when`)
			return execErr
		})
	})
}

// TestAnImportThatFailsPartWayImportsNothing.
//
// ADR-0022 decided that a CSV import writes every valid row or none, on the
// grounds that 340 rows of 400 leaves somebody with a reconciliation problem
// rather than a timesheet. For a long time that was the intention rather than
// the mechanism: the rows were written one at a time, each in its own
// transaction, and the ADR said so in its consequences - "a genuine
// single-transaction import is the improvement this design still wants".
//
// This is that improvement, asserted. The failure is injected against the third
// row specifically, so the first two have already been written when it happens.
func TestAnImportThatFailsPartWayImportsNothing(t *testing.T) {
	f := newFixture(t)

	const file = "date,assignment,hours,note\n" +
		"2026-03-16,Development,1.5,first\n" +
		"2026-03-17,Development,2,second\n" +
		"2026-03-18,Development,1,the-one-that-fails\n"

	breakInsertsWhen(t, f.db, "time_entries", "NEW.note = 'the-one-that-fails'")

	imported, err := f.svc.ImportTimeCSV(f.ctx, strings.NewReader(file), false)
	if err == nil {
		t.Fatal("an import whose third row could not be written reported success")
	}
	if !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("the import failed for the wrong reason: %v", err)
	}
	if imported != 0 {
		t.Errorf("the import reported %d rows written after failing", imported)
	}

	if rows := countRows(t, f.db, "time_entries", ""); rows != 0 {
		t.Errorf("%d of the earlier rows survived a failed import. All or nothing "+
			"is the whole design (ADR-0022): a partial import leaves somebody "+
			"comparing two sources row by row.", rows)
	}
	// And no summary row claiming an import that did not happen.
	if rows := countRows(t, f.db, "audit_events", "action = ?", "time_entry.import"); rows != 0 {
		t.Errorf("the trail records %d imports that did not happen", rows)
	}
}

// TestAFailedAttachmentLeavesOnlyAnOrphanedFile.
//
// The bytes are written to the blob store before the row, deliberately: they are
// content-addressed, so a crash between the two leaves a file nothing references
// rather than a row pointing at nothing. That ordering means a rolled-back
// attachment leaves the bytes on disk, and the orphan sweep is what collects
// them - which is only true if the sweep can actually see them.
func TestAFailedAttachmentLeavesOnlyAnOrphanedFile(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	entry := mustCreate(t, f, f.now, 3600)

	breakInserts(t, f.db, "audit_events")

	if _, err := f.svc.Attach(f.ctx, "time_entry", entry.ID,
		"receipt.txt", strings.NewReader("some bytes")); err == nil {
		t.Fatal("attaching a file succeeded with the audit write failing")
	}
	if rows := countRows(t, f.db, "attachments", ""); rows != 0 {
		t.Errorf("%d attachment rows survived a failed audit write", rows)
	}

	// The sweep is the other half of the arrangement: without it, the bytes
	// would sit on disk forever with nothing pointing at them.
	swept, err := f.svc.SweepBlobs(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Errorf("the sweep collected %d files, want the one the rollback orphaned", swept)
	}
}

// TestDeletingAnAttachmentRemovesTheBytesOnlyAfterTheRowIsGone.
//
// The order matters in both directions. The row and its audit entry commit
// together; the bytes go afterwards, because removing them first would leave any
// other row sharing that file - content addressing means several records can -
// pointing at nothing.
func TestDeletingAnAttachmentRemovesTheBytesOnlyAfterTheRowIsGone(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	entry := mustCreate(t, f, f.now, 3600)

	attachment, err := f.svc.Attach(f.ctx, "time_entry", entry.ID,
		"receipt.txt", strings.NewReader("some bytes"))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	breakInserts(t, f.db, "audit_events")

	if err := f.svc.DeleteAttachment(f.ctx, attachment.ID); err == nil {
		t.Fatal("deleting an attachment succeeded with the audit write failing")
	}
	if rows := countRows(t, f.db, "attachments", ""); rows != 1 {
		t.Errorf("the attachment row is gone after a failed audit write")
	}

	// The bytes are still there, which is what makes the surviving row mean
	// something: a row pointing at a file that was already deleted would be
	// worse than either half alone.
	if _, _, err := f.svc.OpenAttachment(f.ctx, attachment.ID); err != nil {
		t.Errorf("the file was removed for a deletion that rolled back: %v", err)
	}
}

// The account and timesheet paths, which need the server-mode fixture: the RBAC
// authoriser with a real membership lookup, and the account service on top.
//
// These are the changes with the widest consequences in the application. A role
// change decides what somebody can see; a password change is somebody trying to
// end an intrusion; an approval is the figure that goes on an invoice. Each one
// is also a change that forces a second write - a privilege change signs the
// account out, a password change signs it out everywhere - and those belong in
// the same transaction as the change itself, or a partial commit leaves an
// account whose sessions and whose privileges disagree.

// TestEveryAccountAndTimesheetMutationRollsBackTogether.
func TestEveryAccountAndTimesheetMutationRollsBackTogether(t *testing.T) {
	for _, mutation := range []struct {
		name  string
		table string
		setup func(t *testing.T, f *serverFixture)
		do    func(t *testing.T, f *serverFixture) error
	}{
		{
			name:  "create a user",
			table: "users",
			do: func(t *testing.T, f *serverFixture) error {
				_, err := f.accounts.CreateUser(f.ctx, NewUserInput{
					DisplayName: "Rolled Back", Email: "rolled@example.com",
					Password: "a-long-enough-password", Role: domain.RoleMember,
				})
				return err
			},
		},
		{
			name:  "add somebody to a project",
			table: "project_members",
			setup: func(t *testing.T, f *serverFixture) { f.team(t) },
			do: func(t *testing.T, f *serverFixture) error {
				return f.accounts.AddMember(f.ctx, Membership{ProjectID: 1, UserID: f.admin.ID})
			},
		},
		{
			name:  "remove somebody from a project",
			table: "project_members",
			setup: func(t *testing.T, f *serverFixture) { f.team(t) },
			do: func(t *testing.T, f *serverFixture) error {
				return f.accounts.RemoveMember(f.ctx, 1, f.admin.ID)
			},
		},
		{
			name:  "submit a week",
			table: "timesheet_periods",
			setup: func(t *testing.T, f *serverFixture) {
				assignment, _ := f.team(t)
				recordFor(t, f, assignment.ID, f.now)
			},
			do: func(t *testing.T, f *serverFixture) error {
				_, err := f.svc.SubmitWeek(f.ctx, f.now)
				return err
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newServerFixture(t)
			if mutation.setup != nil {
				mutation.setup(t, f)
			}
			before := countRows(t, f.db, mutation.table, "")

			breakInserts(t, f.db, "audit_events")

			err := mutation.do(t, f)
			if err == nil {
				t.Fatalf("%s succeeded with the audit write failing", mutation.name)
			}
			if !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("%s failed for the wrong reason: %v", mutation.name, err)
			}
			if after := countRows(t, f.db, mutation.table, ""); after != before {
				t.Errorf("%s survived a failed audit write: %d rows before, %d after",
					mutation.name, before, after)
			}
		})
	}
}

// TestADecisionOnAWeekRollsBackWithItsRecord.
//
// An approval is not a row count - the week is already there and its status
// changes - so this is asserted on the status. It is the audit row somebody
// reads to answer "who approved this", months later, with an invoice in front of
// them.
func TestADecisionOnAWeekRollsBackWithItsRecord(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	// The colleague records a week and submits it.
	colleagueCtx := f.asUser(colleague)
	if _, err := f.svc.CreateEntry(colleagueCtx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: f.now, DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record time: %v", err)
	}
	if _, err := f.svc.SubmitWeek(colleagueCtx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	weekStart := domain.WeekStartFor(f.now, 1, time.UTC)
	breakInserts(t, f.db, "audit_events")

	if err := f.svc.ApproveWeek(f.ctx, colleague.ID, weekStart); err == nil {
		t.Fatal("approving a week succeeded with the audit write failing")
	}

	period, err := f.db.GetPeriod(context.Background(), colleague.ID, weekStart)
	if err != nil {
		t.Fatalf("read the period: %v", err)
	}
	if period.Status != domain.PeriodSubmitted {
		t.Errorf("the week is %s after an approval that could not be recorded; an "+
			"approval nobody can attribute is the one thing an approval must not be",
			period.Status)
	}
}

// TestAFailedPasswordChangeLeavesTheAccountExactlyAsItWas.
//
// Three writes that have to be one: the new hash, the revocation of every other
// session, and the record of who did it. Any partial commit is its own kind of
// wrong - a password that changed with the old sessions still live, sessions
// revoked against a password that did not change, or either of those with
// nothing in the trail - and this is the operation somebody performs when they
// think an account has been taken over.
func TestAFailedPasswordChangeLeavesTheAccountExactlyAsItWas(t *testing.T) {
	f := newServerFixture(t)

	user, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Somebody", Email: "somebody@example.com",
		Password: "the-original-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	login, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "somebody@example.com", Password: "the-original-password", IP: "203.0.113.4",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	sessionsBefore := countRows(t, f.db, "sessions", "user_id = ?", user.ID)
	if sessionsBefore == 0 {
		t.Fatal("the login left no session to revoke")
	}

	breakInserts(t, f.db, "audit_events")

	if err := f.accounts.SetPassword(f.ctx, user.ID, "", "a-different-password"); err == nil {
		t.Fatal("setting a password succeeded with the audit write failing")
	}

	// The old password still works, which is the same as saying the hash was not
	// written.
	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "somebody@example.com", Password: "the-original-password", IP: "203.0.113.4",
	}); err != nil {
		t.Errorf("the original password no longer works after a password change "+
			"that reported failure: %v", err)
	}
	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "somebody@example.com", Password: "a-different-password", IP: "203.0.113.4",
	}); err == nil {
		t.Error("the new password works after a password change that reported failure")
	}

	// And the session that existed before is still there: revoked sessions
	// without a changed password would sign somebody out for nothing.
	if _, _, err := f.accounts.ResolveSession(context.Background(), login.CookieValue); err != nil {
		t.Errorf("the existing session was revoked by a password change that "+
			"rolled back: %v", err)
	}
}

// TestAFailedRoleChangeLeavesTheSessionsAlone.
//
// The other paired write. Changing a role signs the account out, because a
// privilege change that leaves the old sessions alive has not taken effect - so
// the two must not come apart in either direction.
func TestAFailedRoleChangeLeavesTheSessionsAlone(t *testing.T) {
	f := newServerFixture(t)

	user, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Somebody", Email: "somebody@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "somebody@example.com", Password: "a-long-enough-password", IP: "203.0.113.4",
	}); err != nil {
		t.Fatalf("login: %v", err)
	}

	breakInserts(t, f.db, "audit_events")

	user.Role = domain.RoleManager
	if err := f.accounts.UpdateUser(f.ctx, user); err == nil {
		t.Fatal("changing a role succeeded with the audit write failing")
	}

	after, err := f.db.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("read the user: %v", err)
	}
	if after.Role != domain.RoleMember {
		t.Errorf("the role is %s after a change that could not be recorded", after.Role)
	}
	if sessions := countRows(t, f.db, "sessions", "user_id = ?", user.ID); sessions != 1 {
		t.Errorf("%d sessions after a rolled-back role change, want the one that "+
			"existed: the sign-out and the change are one operation", sessions)
	}
}

// recordFor records an hour against an assignment, for the fixtures above.
func recordFor(t *testing.T, f *serverFixture, assignmentID int64, at time.Time) {
	t.Helper()

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignmentID, StartedAt: at, DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record time: %v", err)
	}
}
