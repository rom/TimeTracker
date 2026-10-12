package store

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The transactional writes, against an executor that cannot work.
//
// Every ...Tx function here exists so that a change and the audit row recording
// it land together or not at all. The service layer's mutate() builds on that:
// it runs the change and the audit insert on one transaction and relies on
// either failing to abort the other.
//
// Which means a ...Tx function that swallowed its error would be worse than one
// that crashed. The change would silently not happen, mutate() would see no
// error, the audit row would be written anyway, and the log would record a
// change the database never made. Nobody would find out until somebody asked
// why the audit trail and the data disagree.
//
// So: hand every one of them a transaction that has already been rolled back,
// and require an error from each. It is a uniform assertion, which is the point
// - a write added later and listed here is checked for free, and one that is
// not listed shows up as a gap in this package's coverage.
func TestEveryTransactionalWriteReportsAFailureRatherThanSwallowingIt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	entry := entryAt(t, db, user, assignment,
		time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), 3600, domain.StatusConfirmed)
	settings, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	// A transaction that has been rolled back. Every statement on it fails the
	// way a statement on a broken connection does, without needing a broken
	// connection.
	dead := func(t *testing.T) *sql.Tx {
		t.Helper()
		tx, err := db.Reader().BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("roll back: %v", err)
		}
		return tx
	}

	writes := map[string]func(tx *sql.Tx) error{
		"CreateCustomerTx": func(tx *sql.Tx) error {
			_, err := CreateCustomerTx(ctx, tx, domain.Customer{Name: "Nope", Currency: "EUR"})
			return err
		},
		"UpdateCustomerTx": func(tx *sql.Tx) error {
			return UpdateCustomerTx(ctx, tx, domain.Customer{ID: project.CustomerID, Name: "Nope", Currency: "EUR"})
		},
		"SetCustomerArchivedTx": func(tx *sql.Tx) error {
			return SetCustomerArchivedTx(ctx, tx, project.CustomerID, true)
		},
		"CreateProjectTx": func(tx *sql.Tx) error {
			_, err := CreateProjectTx(ctx, tx, domain.Project{CustomerID: project.CustomerID, Name: "Nope"})
			return err
		},
		"UpdateProjectTx": func(tx *sql.Tx) error {
			return UpdateProjectTx(ctx, tx, project)
		},
		"SetProjectArchivedTx": func(tx *sql.Tx) error {
			return SetProjectArchivedTx(ctx, tx, project.ID, true)
		},
		"CreateAssignmentTx": func(tx *sql.Tx) error {
			_, err := CreateAssignmentTx(ctx, tx, domain.Assignment{ProjectID: project.ID, Name: "Nope"})
			return err
		},
		"UpdateAssignmentTx": func(tx *sql.Tx) error {
			return UpdateAssignmentTx(ctx, tx, assignment)
		},
		"SetAssignmentArchivedTx": func(tx *sql.Tx) error {
			return SetAssignmentArchivedTx(ctx, tx, assignment.ID, true)
		},
		"UpdateSettingsTx": func(tx *sql.Tx) error {
			return UpdateSettingsTx(ctx, tx, settings)
		},
		"UpdateUserPreferencesTx": func(tx *sql.Tx) error {
			return UpdateUserPreferencesTx(ctx, tx, user.ID, "dark", "UTC", "en")
		},
		"UpdateUserAdminTx": func(tx *sql.Tx) error {
			return UpdateUserAdminTx(ctx, tx, user)
		},
		"CreateAccountTx": func(tx *sql.Tx) error {
			_, err := CreateAccountTx(ctx, tx, Account{
				User: domain.User{DisplayName: "Nope", Role: domain.RoleMember,
					TimeZone: "UTC", Theme: "light", Active: true},
			})
			return err
		},
		"SetPasswordHashTx": func(tx *sql.Tx) error {
			return SetPasswordHashTx(ctx, tx, user.ID, "irrelevant")
		},
		"AddProjectMemberTx": func(tx *sql.Tx) error {
			return AddProjectMemberTx(ctx, tx, ProjectMember{ProjectID: project.ID, UserID: user.ID})
		},
		"RemoveProjectMemberTx": func(tx *sql.Tx) error {
			return RemoveProjectMemberTx(ctx, tx, project.ID, user.ID)
		},
		"CreateEntryTx": func(tx *sql.Tx) error {
			_, err := CreateEntryTx(ctx, tx, entry)
			return err
		},
		"CreateEntryWithTagsTx": func(tx *sql.Tx) error {
			_, err := CreateEntryWithTagsTx(ctx, tx, entry)
			return err
		},
		"UpdateEntryTx": func(tx *sql.Tx) error {
			return UpdateEntryTx(ctx, tx, entry)
		},
		"UpdateEntryWithTagsTx": func(tx *sql.Tx) error {
			return UpdateEntryWithTagsTx(ctx, tx, entry)
		},
		"StopEntryTx": func(tx *sql.Tx) error {
			_, err := StopEntryTx(ctx, tx, entry.ID, time.Now(), 3600, BillingSnapshot{Currency: "EUR"})
			return err
		},
		"GetEntryTx": func(tx *sql.Tx) error {
			_, err := GetEntryTx(ctx, tx, entry.ID)
			return err
		},
		"InsertAuditTx": func(tx *sql.Tx) error {
			return InsertAuditTx(ctx, tx, AuditEvent{
				At: time.Now(), ActorID: user.ID, Action: "update",
				ResourceType: "time_entry", ResourceID: entry.ID,
			})
		},
		"IndexEntryTx": func(tx *sql.Tx) error {
			return IndexEntryTx(ctx, tx, entry.ID)
		},
		"ReindexSearchTx": func(tx *sql.Tx) error {
			_, err := ReindexSearchTx(ctx, tx)
			return err
		},
		"EnsureTagsTx": func(tx *sql.Tx) error {
			_, err := EnsureTagsTx(ctx, tx, []string{"incident"})
			return err
		},
		"SetEntryTagsTx": func(tx *sql.Tx) error {
			return SetEntryTagsTx(ctx, tx, entry.ID, []string{"incident"})
		},
		"UpdateTagTx": func(tx *sql.Tx) error {
			return UpdateTagTx(ctx, tx, domain.Tag{ID: 1, Name: "outage"})
		},
		"DeleteTagTx": func(tx *sql.Tx) error {
			return DeleteTagTx(ctx, tx, 1)
		},
		"CreateExpenseTx": func(tx *sql.Tx) error {
			_, err := CreateExpenseTx(ctx, tx, domain.Expense{
				UserID: user.ID, EnteredBy: user.ID, ProjectID: project.ID,
				SpentOn: "2026-03-16", Category: "travel", AmountMinor: 1000,
				Currency: "EUR", Status: domain.StatusConfirmed,
			})
			return err
		},
		"UpdateExpenseTx": func(tx *sql.Tx) error {
			return UpdateExpenseTx(ctx, tx, domain.Expense{ID: 1, ProjectID: project.ID,
				SpentOn: "2026-03-16", Currency: "EUR", Status: domain.StatusConfirmed})
		},
		"DeleteExpenseTx": func(tx *sql.Tx) error {
			return DeleteExpenseTx(ctx, tx, 1)
		},
		"CreateAttachmentTx": func(tx *sql.Tx) error {
			_, err := CreateAttachmentTx(ctx, tx, domain.Attachment{
				OwnerType: "time_entry", OwnerID: entry.ID, SHA256: "abc",
				Filename: "receipt.pdf", MIME: "application/pdf", SizeBytes: 1,
				UploadedBy: user.ID,
			})
			return err
		},
		"DeleteAttachmentTx": func(tx *sql.Tx) error {
			_, err := DeleteAttachmentTx(ctx, tx, 1, "abc")
			return err
		},
		"UpsertPeriodTx": func(tx *sql.Tx) error {
			return UpsertPeriodTx(ctx, tx, domain.TimesheetPeriod{
				UserID: user.ID, WeekStart: "2026-03-16", Status: domain.PeriodSubmitted,
			})
		},
		"ResolveIdleObservationTx": func(tx *sql.Tx) error {
			return ResolveIdleObservationTx(ctx, tx, 1, domain.IdleKeep, time.Now())
		},
		"ResolveIdleObservationsForEntryTx": func(tx *sql.Tx) error {
			return ResolveIdleObservationsForEntryTx(ctx, tx, entry.ID, domain.IdleKeep, time.Now())
		},
		"CreateContractTermsTx": func(tx *sql.Tx) error {
			_, err := CreateContractTermsTx(ctx, tx, domain.ContractTerms{
				Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
				EffectiveFrom: "2026-01-01",
			})
			return err
		},
		"UpdateContractTermsTx": func(tx *sql.Tx) error {
			return UpdateContractTermsTx(ctx, tx, domain.ContractTerms{
				ID: 1, Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
			})
		},
		"DeleteContractTermsTx": func(tx *sql.Tx) error {
			return DeleteContractTermsTx(ctx, tx, 1)
		},
		"DeleteSessionTx": func(tx *sql.Tx) error {
			return DeleteSessionTx(ctx, tx, "hash")
		},
		"DeleteUserSessionsTx": func(tx *sql.Tx) error {
			_, err := DeleteUserSessionsTx(ctx, tx, user.ID)
			return err
		},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			if err := write(dead(t)); err == nil {
				t.Errorf("%s reported success on a transaction that cannot execute - "+
					"an atomic change built on this would record an audit row for "+
					"work the database never did", name)
			}
		})
	}

	// The table above is only as good as its completeness, and a list in a test
	// file is exactly the kind of thing that stops being complete the week
	// after it is written. So read the package's own source for the writes it
	// exports: one added without being listed here fails this rather than
	// quietly reducing what is checked.
	for _, name := range exportedTransactionalWrites(t) {
		if writes[name] == nil {
			t.Errorf("%s is an exported transactional write and is not in this "+
				"test's table, so nothing checks that it reports a failure", name)
		}
	}
}

// exportedTransactionalWrites reads this package's source for the names of its
// exported ...Tx functions.
//
// Parsing rather than reflecting: a package cannot enumerate its own functions
// at run time, and the alternative - a second hand-written list - would go
// stale in the same way and for the same reason as the first.
func exportedTransactionalWrites(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	var names []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			// Methods are excluded: a method on *DB writes on the store's own
			// connection and is not something a caller composes into a
			// transaction of their own.
			if !ok || fn.Recv != nil || !fn.Name.IsExported() {
				continue
			}
			if strings.HasSuffix(fn.Name.Name, "Tx") {
				names = append(names, fn.Name.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}
