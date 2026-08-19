package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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

	// A window wide enough to include both entries: the query is bounded now,
	// and the bound is what the test below is not about.
	recent, err := db.RecentAssignments(ctx, user.ID, base.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent assignments, got %d", len(recent))
	}
	if recent[0].ID != second.ID {
		t.Errorf("most recently used assignment should sort first, got %q", recent[0].Name)
	}

	// The window is a real bound, not decoration. Anything older than it is not
	// a suggestion anybody wants, and leaving it unbounded made the day screen
	// group a whole history to rank a handful of assignments.
	narrow, err := db.RecentAssignments(ctx, user.ID, base.Add(12*time.Hour), 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(narrow) != 1 || narrow[0].ID != second.ID {
		t.Errorf("a window starting after the first entry should return only the "+
			"second, got %d assignments", len(narrow))
	}
}

// TestMigrationsUpgradeExistingData is the test the fresh-database one cannot
// be.
//
// A migration that carries data forward only executes when there is data to
// carry, so a suite that always starts from an empty database never runs those
// statements at all. 0007 moved the contract terms off the customer table, and
// wrote their timestamps with SQLite's datetime() - which separates the date
// and the time with a space, a format the reader refuses. Nothing on a fresh
// database could have noticed.
//
// This applies the schema in two halves with real rows in between, which is
// what an upgrade actually is.
func TestMigrationsUpgradeExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")

	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(all) < 2 {
		t.Skip("needs at least two migrations to have an upgrade path")
	}

	// Build the previous schema, put real rows in it, then let Open apply the
	// rest - which is what an upgrade is.
	previous := all[len(all)-2].version
	old, err := openAt(ctx, path, previous)
	if err != nil {
		t.Fatalf("build the previous schema: %v", err)
	}
	seedForUpgrade(ctx, t, old, previous)
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = db.Close() }()

	if version, err := db.SchemaVersion(ctx); err != nil {
		t.Fatalf("schema version: %v", err)
	} else if version != all[len(all)-1].version {
		t.Errorf("after upgrade the version is %d, want %d", version, all[len(all)-1].version)
	}

	assertTimestampsParse(ctx, t, db)
}

// seedForUpgrade puts rows in the old schema that its successor has to carry
// forward. Version-specific by necessity: what an upgrade must preserve depends
// on which upgrade it is.
func seedForUpgrade(ctx context.Context, t *testing.T, db *DB, version int) {
	t.Helper()

	if version == 6 {
		// 0007 moves contract terms off the customer table. Only a customer
		// that actually has terms exercises the statement that moves them.
		if _, err := db.write.ExecContext(ctx, `
			INSERT INTO customers (name, currency, rate_minor, created_at,
			    overtime_multiplier_pct, travel_billing, mileage_rate_minor)
			VALUES ('Acme', 'SEK', 100000, ?, 150, 'rate', 2500)`,
			formatTime(time.Now())); err != nil {
			t.Fatalf("seed a customer with terms: %v", err)
		}
	}
}

// assertTimestampsParse reads every timestamp column in the database back
// through the parser the application uses.
//
// A stored timestamp the reader refuses takes a whole screen down, and it is
// invisible until somebody has a row of that kind - which for a data-carrying
// migration means invisible until an upgrade in production.
func assertTimestampsParse(ctx context.Context, t *testing.T, db *DB) {
	t.Helper()

	tables, err := db.read.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	_ = tables.Close()

	for _, table := range names {
		columns, err := db.read.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("columns of %s: %v", table, err)
		}
		var timestamps []string
		for columns.Next() {
			var name string
			if err := columns.Scan(&name); err != nil {
				t.Fatalf("scan column: %v", err)
			}
			// The convention throughout is that an instant column ends in _at.
			if strings.HasSuffix(name, "_at") {
				timestamps = append(timestamps, name)
			}
		}
		_ = columns.Close()

		for _, column := range timestamps {
			rows, err := db.read.QueryContext(ctx,
				`SELECT `+column+` FROM `+table+` WHERE `+column+` IS NOT NULL AND `+column+` <> ''`)
			if err != nil {
				t.Fatalf("read %s.%s: %v", table, column, err)
			}
			for rows.Next() {
				var value string
				if err := rows.Scan(&value); err != nil {
					t.Fatalf("scan %s.%s: %v", table, column, err)
				}
				if _, err := parseTime(value); err != nil {
					t.Errorf("%s.%s holds %q, which the application cannot read: %v",
						table, column, value, err)
				}
			}
			_ = rows.Close()
		}
	}
}

// TestMigrationsWriteTimestampsInTheStoredFormat is the cheap general guard for
// the same defect.
//
// SQLite's datetime() and date() produce a space-separated form; every
// timestamp this application writes is RFC 3339. A migration that populates a
// timestamp has to say so explicitly, and this catches the one that forgets
// without needing an upgrade with the right data in it.
func TestMigrationsWriteTimestampsInTheStoredFormat(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	for _, entry := range entries {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			// Comments explain the rule; they are not breaking it.
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if strings.Contains(line, "datetime('now')") || strings.Contains(line, "date('now')") {
				t.Errorf("%s writes a timestamp with datetime()/date(), which is not "+
					"the RFC 3339 form the application reads:\n  %s",
					entry.Name(), strings.TrimSpace(line))
			}
		}
	}
}

// TestTagStorage covers the join table and the page-at-a-time load.
//
// The load matters: rendering a day asks for the tags of a few dozen entries,
// and a query per entry is the N+1 that turns a fast screen slow as soon as
// somebody has a real week.
func TestTagStorage(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	user, assignment := seed(t, db)

	var ids []int64
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		for i, tags := range [][]string{
			{"incident", "urgent"},
			{"incident"},
			nil,
		} {
			entry, err := CreateEntryWithTagsTx(ctx, tx, domain.TimeEntry{
				UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
				StartedAt:       time.Date(2026, 3, 16, 9+i, 0, 0, 0, time.UTC),
				DurationSeconds: 3600, Status: domain.StatusConfirmed,
				TimeZone: "UTC", Tags: tags,
			})
			if err != nil {
				return err
			}
			ids = append(ids, entry.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create entries: %v", err)
	}

	// One query for the page, not one per entry.
	byEntry, err := db.TagsForEntries(ctx, ids)
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}
	if len(byEntry[ids[0]]) != 2 {
		t.Errorf("entry tags = %v, want two", byEntry[ids[0]])
	}

	// The tag list carries usage counts, which is what makes the management
	// screen able to say which tags are worth keeping.
	tags, err := db.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %+v, want incident and urgent", tags)
	}
	// Two entries carry "incident", one carries "urgent", one carries nothing.
	wantCounts := map[string]int{"incident": 2, "urgent": 1}
	for _, tag := range tags {
		if tag.EntryCount != wantCounts[tag.Name] {
			t.Errorf("%s has count %d, want %d", tag.Name, tag.EntryCount, wantCounts[tag.Name])
		}
	}

	// Deleting a tag takes its links with it through the cascade, and leaves
	// the entries alone.
	if err := db.DeleteTag(ctx, tags[0].ID); err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	if byEntry, err = db.TagsForEntries(ctx, ids); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, names := range byEntry {
		for _, name := range names {
			if name == tags[0].Name {
				t.Errorf("a deleted tag is still attached: %s", name)
			}
		}
	}
	if _, err := db.GetEntry(ctx, ids[0]); err != nil {
		t.Errorf("the entry went with its tag: %v", err)
	}
}

// TestContractTermsStorage covers the dated lookup and the project-over-customer
// merge, at the level that actually runs the SQL.
func TestContractTermsStorage(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	_, assignment := seed(t, db)

	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	for _, terms := range []domain.ContractTerms{
		{Scope: domain.TermsForCustomer, ScopeID: project.CustomerID, EffectiveFrom: "",
			Rules: domain.RateRules{OvertimeMultiplierPct: 150, MileageRateMinor: 2500}},
		{Scope: domain.TermsForCustomer, ScopeID: project.CustomerID, EffectiveFrom: "2026-07-01",
			Rules: domain.RateRules{OvertimeMultiplierPct: 200}},
		{Scope: domain.TermsForProject, ScopeID: project.ID, EffectiveFrom: "",
			Rules: domain.RateRules{TravelBilling: domain.TravelUnbilled}},
	} {
		if _, err := db.CreateContractTerms(ctx, terms); err != nil {
			t.Fatalf("create terms: %v", err)
		}
	}

	// Before the second revision: the open-ended customer terms, with the
	// project's travel rule laid over them.
	early, err := db.ResolveTerms(ctx, project.CustomerID, project.ID, "2026-03-16")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if early.OvertimeMultiplierPct != 150 {
		t.Errorf("overtime = %d, want 150", early.OvertimeMultiplierPct)
	}
	if early.TravelBilling != domain.TravelUnbilled {
		t.Errorf("travel = %q, want the project's rule", early.TravelBilling)
	}
	if early.MileageRateMinor != 2500 {
		t.Errorf("mileage = %d, want the customer's", early.MileageRateMinor)
	}

	// After it, the newer customer revision applies and the project's rule
	// still does.
	late, err := db.ResolveTerms(ctx, project.CustomerID, project.ID, "2026-08-01")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if late.OvertimeMultiplierPct != 200 {
		t.Errorf("overtime = %d, want the July revision's 200", late.OvertimeMultiplierPct)
	}
	if late.TravelBilling != domain.TravelUnbilled {
		t.Errorf("travel = %q, want the project's rule still", late.TravelBilling)
	}

	// Listing is newest first, which is the order resolution walks.
	list, err := db.ListContractTerms(ctx, domain.TermsForCustomer, project.CustomerID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].EffectiveFrom != "2026-07-01" {
		t.Errorf("list = %+v, want the newest revision first", list)
	}
}

// TestRoutineStorage round-trips the fields that are encoded rather than stored
// directly: the weekday list and the tag list.
func TestRoutineStorage(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	user, assignment := seed(t, db)

	created, err := db.CreateRoutine(ctx, domain.Routine{
		UserID: user.ID, AssignmentID: assignment.ID, Name: "Stand-up",
		Weekdays: []int{1, 3, 5}, StartTime: "09:15",
		DurationSeconds: 900, Tags: []string{"admin"}, Active: true,
		Kind: domain.KindWork,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	loaded, err := db.GetRoutine(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(loaded.Weekdays) != 3 || loaded.Weekdays[0] != 1 || loaded.Weekdays[2] != 5 {
		t.Errorf("weekdays = %v, want [1 3 5]", loaded.Weekdays)
	}
	if len(loaded.Tags) != 1 || loaded.Tags[0] != "admin" {
		t.Errorf("tags = %v", loaded.Tags)
	}
	if loaded.AssignmentName == "" || loaded.CustomerName == "" {
		t.Error("the routine did not come back with its assignment's names")
	}

	// Inactive routines are kept but excluded from the active listing: the
	// thing that happens every week until the project it belongs to pauses.
	loaded.Active = false
	if err := db.UpdateRoutine(ctx, loaded); err != nil {
		t.Fatalf("update: %v", err)
	}
	active, err := db.ListRoutines(ctx, user.ID, true)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("an inactive routine is still offered: %+v", active)
	}
	all, err := db.ListRoutines(ctx, user.ID, false)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("the inactive routine was lost: %+v", all)
	}
}
