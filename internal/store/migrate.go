package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFS carries the schema inside the binary, so a copied executable can
// create its own database with no files beside it.
// See docs/adr/0009-embedded-assets-and-migrations.md.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one numbered schema step.
type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

// migrate applies every outstanding migration, in order, each in its own
// transaction.
//
// Migrations are forward-only by decision: a down migration either does nothing
// interesting or destroys data, and is invariably the least-tested code in the
// repository. Recovery from a bad migration is by restoring a backup, which is a
// path we test. See docs/adr/0009-embedded-assets-and-migrations.md.

// allMigrations means "apply everything available".
const allMigrations = 1 << 30

// migrateTo applies migrations up to and including a version.
//
// The cap exists for the tests. A migration that carries existing data forward
// only executes its data statements when there is data, so a suite that always
// starts from an empty database never runs them; testing an upgrade means
// building the old schema, putting rows in, and then applying the rest.
func (db *DB) migrateTo(ctx context.Context, maxVersion int) error {
	if _, err := db.write.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			checksum   TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	available, err := loadMigrations()
	if err != nil {
		return err
	}
	applied, err := db.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	// A database migrated by a newer binary must not be touched by an older one:
	// the older binary does not know the newer schema, and writing to it would
	// corrupt data in ways no error message would explain.
	highestAvailable := 0
	if len(available) > 0 {
		highestAvailable = available[len(available)-1].version
	}
	for version := range applied {
		if version > highestAvailable {
			return fmt.Errorf(
				"database schema version %d is newer than this binary understands (%d); "+
					"upgrade the application rather than downgrading the data",
				version, highestAvailable)
		}
	}

	var pending []migration
	for _, m := range available {
		if m.version > maxVersion {
			continue
		}
		recorded, ok := applied[m.version]
		if !ok {
			pending = append(pending, m)
			continue
		}
		// An already-applied migration whose content has changed means someone
		// edited history. Two installations would silently end up with different
		// schemas, so this is a hard failure at the only moment it is still cheap
		// to notice.
		if recorded != m.checksum {
			return fmt.Errorf(
				"migration %04d_%s has been modified since it was applied "+
					"(recorded checksum %s, file checksum %s); applied migrations are immutable",
				m.version, m.name, recorded[:8], m.checksum[:8])
		}
	}

	if len(pending) == 0 {
		return nil
	}

	// Take a copy of the database before changing its shape, so a failed upgrade
	// is recoverable even from a user who has never taken a backup.
	if len(applied) > 0 {
		if err := db.backupBeforeMigration(); err != nil {
			return fmt.Errorf("pre-migration backup: %w", err)
		}
	}

	for _, m := range pending {
		if err := db.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and records it, atomically.
func (db *DB) applyMigration(ctx context.Context, m migration) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", m.version, m.name, err)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			m.version, m.name, m.checksum, formatTime(time.Now()))
		if err != nil {
			return fmt.Errorf("record migration %04d_%s: %w", m.version, m.name, err)
		}
		return nil
	})
}

// appliedMigrations reads the versions already applied, with their checksums.
func (db *DB) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := db.write.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		applied[version] = checksum
	}
	return applied, rows.Err()
}

// loadMigrations reads the embedded migration files, sorted by version.
//
// File names are "NNNN_description.sql". The number is authoritative; the
// description exists for humans and appears in error messages.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		numberPart, namePart, found := strings.Cut(strings.TrimSuffix(entry.Name(), ".sql"), "_")
		if !found {
			return nil, fmt.Errorf("migration %q must be named NNNN_description.sql", entry.Name())
		}
		version, err := strconv.Atoi(numberPart)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", entry.Name(), err)
		}

		content, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(content)

		migrations = append(migrations, migration{
			version:  version,
			name:     namePart,
			sql:      string(content),
			checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})

	// Duplicate version numbers mean two people added a migration at the same
	// time and one of them will be skipped depending on file ordering. Catch it
	// here rather than in production.
	for i := 1; i < len(migrations); i++ {
		if migrations[i].version == migrations[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %04d", migrations[i].version)
		}
	}
	return migrations, nil
}

// backupBeforeMigration copies the database file next to itself with a timestamp.
//
// This is a plain file copy rather than SQLite's online backup, which is safe
// here specifically because it runs at startup before any request is served, so
// nothing else is writing. A running-database backup uses a different mechanism.
func (db *DB) backupBeforeMigration() error {
	source, err := os.Open(db.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to back up on a first run
		}
		return err
	}
	defer func() { _ = source.Close() }()

	backupPath := fmt.Sprintf("%s.backup-%s",
		db.path, time.Now().UTC().Format("20060102-150405"))
	// 0600: a timesheet backup is no less sensitive than the database itself.
	destination, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = destination.Close() }()

	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	// Force the copy to disk: a backup still sitting in the page cache is no
	// protection against the crash it exists for.
	if err := destination.Sync(); err != nil {
		return err
	}
	_ = filepath.Dir(backupPath) // documented location: beside the database
	return nil
}

// SchemaVersion returns the highest applied migration version, for the version
// command and the health endpoint.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := db.read.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, err
	}
	return int(version.Int64), nil
}
