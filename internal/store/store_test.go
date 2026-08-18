package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// newTestDB opens a database in a temporary directory. Store tests run against
// real SQLite rather than a mock: the behaviour under test *is* the SQL, so a
// mock would only assert that we called ourselves.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	if testing.Short() {
		t.Skip("store tests touch the filesystem; skipped under -short")
	}
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seed creates the minimum hierarchy every entry test needs and returns the ids.
func seed(t *testing.T, db *DB) (user domain.User, assignment domain.Assignment) {
	t.Helper()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Test User", Role: domain.RoleAdmin, TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	customer, err := db.CreateCustomer(ctx, domain.Customer{Name: "Acme", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	project, err := db.CreateProject(ctx, domain.Project{
		CustomerID: customer.ID, Name: "Migration", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	assignment, err = db.CreateAssignment(ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Development", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return user, assignment
}

// TestMigrationsApply covers ASR-010: a fresh database ends up at the expected
// version, and opening it again is a no-op rather than an error.
func TestMigrationsApply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migrate.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if version < 1 {
		t.Fatalf("expected at least one migration applied, got version %d", version)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-opening must be idempotent; a second run of the same migrations would
	// fail on the CREATE TABLE, so this also proves they are not re-applied.
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = db2.Close() }()

	version2, err := db2.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version after reopen: %v", err)
	}
	if version2 != version {
		t.Errorf("version changed on reopen: %d then %d", version, version2)
	}
}

// TestMigrationChecksumIsRecorded proves the tamper detection has something to
// compare against; the failure path is exercised by editing the recorded value.
func TestMigrationChecksumMismatchFailsStartup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "checksum.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Simulate someone having edited an already-applied migration.
	if _, err := db.write.ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'deadbeefdeadbeef' WHERE version = 1`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected startup to fail after an applied migration changed")
	}
}

// TestForeignKeysEnforced: SQLite disables foreign keys per connection by
// default, so this asserts the pragma actually took effect. Without it, orphaned
// rows accumulate silently.
func TestForeignKeysEnforced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.CreateProject(ctx, domain.Project{CustomerID: 99999, Name: "Orphan"})
	if err == nil {
		t.Fatal("expected a foreign key violation for a non-existent customer")
	}
}

func TestCustomerCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	created, err := db.CreateCustomer(ctx, domain.Customer{
		Name: "Acme AB", Code: "ACME", Currency: "SEK", ColourKey: "blue", Icon: "building",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("id not assigned")
	}

	loaded, err := db.GetCustomer(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Name != "Acme AB" || loaded.Currency != "SEK" || loaded.ColourKey != "blue" {
		t.Errorf("round trip lost data: %+v", loaded)
	}
	if loaded.Archived() {
		t.Error("newly created customer should not be archived")
	}

	loaded.Name = "Acme Group AB"
	if err := db.UpdateCustomer(ctx, loaded); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Archiving removes it from the default listing but keeps it retrievable, so
	// historical entries stay readable.
	if err := db.SetCustomerArchived(ctx, created.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	active, err := db.ListCustomers(ctx, UnrestrictedScope(), false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("archived customer still listed: %+v", active)
	}
	all, err := db.ListCustomers(ctx, UnrestrictedScope(), true)
	if err != nil {
		t.Fatalf("list including archived: %v", err)
	}
	if len(all) != 1 || !all[0].Archived() {
		t.Errorf("expected one archived customer, got %+v", all)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.GetCustomer(ctx, 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := db.GetEntry(ctx, 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if err := db.SetProjectArchived(ctx, 12345, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound updating a missing row, got %v", err)
	}
}

// TestConcurrentRunningEntries is the storage-level proof of ASR-001: the schema
// permits several timers to run at once for one user.
func TestConcurrentRunningEntries(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 3; i++ {
		_, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt: start.Add(time.Duration(i) * time.Minute),
			Status:    domain.StatusConfirmed, Billable: true, TimeZone: "UTC",
		})
		if err != nil {
			t.Fatalf("create running entry %d: %v", i, err)
		}
	}

	running, err := db.ListRunningEntries(ctx, user.ID)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 3 {
		t.Fatalf("expected 3 concurrent running timers, got %d", len(running))
	}
	for _, e := range running {
		if !e.Running() {
			t.Error("entry with no end time should report as running")
		}
		if e.AssignmentName == "" || e.CustomerName == "" {
			t.Error("display names not populated by the join")
		}
	}
}

// TestStopEntryIsIdempotent covers the double-submit race: the second stop must
// report that it did nothing rather than corrupting the duration.
func TestStopEntryIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Now().Add(-time.Hour)
	entry, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: start, Status: domain.StatusConfirmed, Billable: true, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	end := start.Add(time.Hour)
	var firstStopped, secondStopped bool
	if err := db.InTx(ctx, func(tx *sql.Tx) error {
		var innerErr error
		firstStopped, innerErr = StopEntryTx(ctx, tx, entry.ID, end, 3600, BillingSnapshot{})
		return innerErr
	}); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if !firstStopped {
		t.Fatal("first stop should have updated the row")
	}

	if err := db.InTx(ctx, func(tx *sql.Tx) error {
		var innerErr error
		secondStopped, innerErr = StopEntryTx(ctx, tx, entry.ID, end.Add(time.Hour), 7200, BillingSnapshot{})
		return innerErr
	}); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if secondStopped {
		t.Error("second stop should have affected no rows")
	}

	reloaded, err := db.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.DurationSeconds != 3600 {
		t.Errorf("duration was overwritten by the second stop: %d", reloaded.DurationSeconds)
	}
}

// TestAuditTrailIsWritten checks the trail can be written and read back for a
// resource, which is the mechanism ASR-006 depends on.
func TestAuditTrailIsWritten(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	err := db.InTx(ctx, func(tx *sql.Tx) error {
		return InsertAuditTx(ctx, tx, AuditEvent{
			At: time.Now(), ActorID: user.ID, ActorName: user.DisplayName,
			Action: "time_entry.create", ResourceType: "time_entry", ResourceID: 7,
			Detail: `{"duration":3600}`,
		})
	})
	if err != nil {
		t.Fatalf("write audit: %v", err)
	}

	events, err := db.ListAuditEvents(ctx, "time_entry", 7, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].Action != "time_entry.create" || events[0].ActorID != user.ID {
		t.Errorf("audit event lost data: %+v", events[0])
	}
}

// TestEntryFilterRanges checks the date bounds are half-open, so a day query
// cannot pick up the first entry of the next day.
func TestEntryFilterRanges(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	day := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	times := []time.Time{
		day.Add(-time.Minute),  // the day before
		day.Add(9 * time.Hour), // during the day
		day.Add(24 * time.Hour) /* exactly midnight the next day */}
	for _, at := range times {
		end := at.Add(time.Hour)
		if _, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt: at, EndedAt: &end, DurationSeconds: 3600,
			Status: domain.StatusConfirmed, Billable: true, TimeZone: "UTC",
		}); err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}

	entries, err := db.ListEntries(ctx, EntryFilter{
		UserID: user.ID, From: day, To: day.Add(24 * time.Hour),
		Scope: UnrestrictedScope(),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("half-open day range should match exactly 1 entry, got %d", len(entries))
	}
}

// TestRecentAssignments orders by most recently used, which is what the quick
// start list depends on.
func TestRecentAssignments(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, first := seed(t, db)

	second, err := db.CreateAssignment(ctx, domain.Assignment{
		ProjectID: first.ProjectID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create second assignment: %v", err)
	}

	base := time.Now().Add(-48 * time.Hour)
	mustEntry := func(assignmentID int64, at time.Time) {
		end := at.Add(time.Hour)
		if _, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignmentID,
			StartedAt: at, EndedAt: &end, DurationSeconds: 3600,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
		}); err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}
	mustEntry(first.ID, base)
	mustEntry(second.ID, base.Add(24*time.Hour)) // more recent

	recent, err := db.RecentAssignments(ctx, user.ID, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent assignments, got %d", len(recent))
	}
	if recent[0].ID != second.ID {
		t.Errorf("most recently used assignment should sort first, got %q", recent[0].Name)
	}
}
