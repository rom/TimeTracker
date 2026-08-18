// Package store owns every SQL statement in the application.
//
// It maps rows to and from the types in internal/domain and knows nothing about
// users, permissions or HTTP: deciding whether a caller is allowed to see a row is
// the service layer's job, not this one's. Keeping SQL in one package is what
// makes "are all queries parameterised?" and "which queries touch entries?"
// answerable by reading a single directory.
//
// See docs/adr/0003-pure-go-sqlite.md for the driver choice and
// docs/adr/0012-layered-package-structure.md for the layering rules.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	// The pure-Go SQLite driver. It is imported for its side effect of
	// registering itself with database/sql. Pure Go rather than the cgo binding
	// is what allows cross-compilation to every supported platform from any
	// host; see docs/adr/0003-pure-go-sqlite.md.
	_ "modernc.org/sqlite"
)

// DB wraps the database handles.
//
// There are two, deliberately. SQLite permits exactly one writer at a time, and
// serialising writes onto a single connection is far simpler than handling
// SQLITE_BUSY at every call site. Reads use a normal pool and proceed
// concurrently, which WAL mode allows without blocking the writer.
type DB struct {
	// write is limited to a single connection; all mutations go through it.
	write *sql.DB
	// read is a pool used for queries.
	read *sql.DB
	path string
}

// Open opens (creating if necessary) the database at path, applies the connection
// settings the application depends on, and runs any outstanding migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	return openAt(ctx, path, allMigrations)
}

// openAt opens a database and migrates it only as far as a version.
//
// The cap exists for the tests, which need to build an older schema, put rows
// in it, and then upgrade - see TestMigrationsUpgradeExistingData. Open is the
// only caller outside them, and it asks for everything.
func openAt(ctx context.Context, path string, maxVersion int) (*DB, error) {
	// The settings below are not optional tuning; the application's correctness
	// assumptions depend on them:
	//
	//   _pragma=foreign_keys(1)  SQLite disables foreign keys per connection by
	//                            default. Without this, a bad delete silently
	//                            orphans rows, which is corruption we would not
	//                            detect until a report looked wrong.
	//   _pragma=journal_mode(WAL) readers do not block the writer, which matters
	//                            as soon as a report runs while a timer stops.
	//   _pragma=busy_timeout(5000) wait for a contended lock rather than failing.
	//   _pragma=synchronous(NORMAL) the standard durability/speed trade-off for
	//                            WAL: safe against process crash, and against
	//                            power loss to the extent WAL allows.
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"

	write, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database for writing: %w", err)
	}
	// One connection, held open: this is what serialises writes.
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)

	read, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("open database for reading: %w", err)
	}
	read.SetMaxOpenConns(8)
	read.SetMaxIdleConns(4)

	db := &DB{write: write, read: read, path: path}

	// Fail fast and clearly if the file is unusable, rather than surfacing the
	// problem later as a mysterious query error.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := write.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to database at %s: %w", path, err)
	}

	if err := db.migrateTo(ctx, maxVersion); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Close releases both handles. A WAL checkpoint is attempted first so the main
// database file is complete for anyone who copies it afterwards.
func (db *DB) Close() error {
	if db.write != nil {
		_, _ = db.write.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	}
	var firstErr error
	for _, handle := range []*sql.DB{db.read, db.write} {
		if handle == nil {
			continue
		}
		if err := handle.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Path returns the database file location, for logging and diagnostics.
func (db *DB) Path() string { return db.path }

// Reader returns the pooled read handle. Callers must not use it for writes.
func (db *DB) Reader() *sql.DB { return db.read }

// InTx runs fn inside a write transaction, committing on success and rolling back
// on error or panic.
//
// The service layer uses this to make a change and its audit row atomic: if the
// audit write fails, the change fails too, so no change can exist without its
// record (docs/adr/0010-audit-log-and-rsyslog.md).
//
// Transactions must stay short. SQLite has a single writer, so a transaction held
// open across an external call would block every other write in the process.
func (db *DB) InTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			// Roll back before re-panicking, otherwise the single write
			// connection is left holding a lock and the process deadlocks.
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// formatTime renders an instant for storage: UTC, RFC 3339 with seconds.
// Every timestamp written by this package goes through here, so the stored format
// cannot drift between tables. See docs/adr/0015-utc-storage-local-display.md.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseTime reads a stored timestamp back. It accepts the nanosecond form as well
// so that a value written by an external tool still loads.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// nullableTime converts an optional stored timestamp into an optional instant.
func nullableTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
