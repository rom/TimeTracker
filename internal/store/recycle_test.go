package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The capture and write-back, at the level where the generic row handling is.
//
// The service tests cover whether a deleted entry comes back whole. These cover
// the mechanism underneath: that a row captured by SELECT * is written back by
// column name, that values survive the JSON round trip as the types SQLite
// stores rather than as floats, and that a payload this version does not
// understand is refused rather than half-applied.

// binFixture gives a database with one entry in it, and its id.
func binFixture(t *testing.T) (*DB, context.Context, domain.User, int64) {
	t.Helper()

	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	entry, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		// A duration past 2^31 would be absurd, but the id and the amount are
		// the fields where a float round trip would bite, so they are given
		// values that would not survive one.
		DurationSeconds: 5400, Note: "captured", Billable: true,
		Status: domain.StatusConfirmed, TimeZone: "UTC",
		AmountMinor: 187500, RateMinor: 125000, Currency: "SEK",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	return db, ctx, user, entry.ID
}

// TestARowSurvivesCaptureAndWriteBack.
func TestARowSurvivesCaptureAndWriteBack(t *testing.T) {
	db, ctx, user, entryID := binFixture(t)

	before, err := db.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	var binID int64
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		binID, txErr = CaptureTx(ctx, tx, DeletedRecord{
			Kind: "time_entry", RecordID: entryID, UserID: user.ID,
			Summary: "captured", DeletedBy: user.ID,
			DeletedAt: time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC),
		}, []Capture{
			{Table: "time_entries", Where: "id = ?", Args: []any{entryID}},
			{Table: "entry_tags", Where: "entry_id = ?", Args: []any{entryID}},
		})
		if txErr != nil {
			return txErr
		}
		_, txErr = tx.ExecContext(ctx, `DELETE FROM time_entries WHERE id = ?`, entryID)
		return txErr
	})
	if err != nil {
		t.Fatalf("capture and delete: %v", err)
	}

	if _, err := db.GetEntry(ctx, entryID); err == nil {
		t.Fatal("the entry is still there after being deleted")
	}

	record, err := db.GetDeleted(ctx, binID)
	if err != nil {
		t.Fatalf("read bin row: %v", err)
	}

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return RestoreTx(ctx, tx, record, []string{"time_entries", "entry_tags"})
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	after, err := db.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}

	// The integers are the point. encoding/json decodes every number into a
	// float64, which would turn an amount of 187500 into 187500.0 and round an
	// id past 2^53 outright; values travel as strings for exactly this reason.
	if after.ID != before.ID || after.DurationSeconds != before.DurationSeconds ||
		after.AmountMinor != before.AmountMinor || after.RateMinor != before.RateMinor {
		t.Errorf("the row did not survive the round trip:\n before %+v\n after  %+v",
			before, after)
	}
	if after.Note != before.Note || after.Currency != before.Currency {
		t.Errorf("text changed: note %q/%q, currency %q/%q",
			after.Note, before.Note, after.Currency, before.Currency)
	}
	if !after.StartedAt.Equal(before.StartedAt) {
		t.Errorf("started_at = %v, want %v", after.StartedAt, before.StartedAt)
	}

	// The bin row is gone: a restored record is not still restorable.
	if _, err := db.GetDeleted(ctx, binID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the bin row survived the restore: %v", err)
	}
}

// TestANullStaysNull.
//
// ended_at is NULL on a running entry, and a capture that turned it into the
// empty string would restore a timer that had stopped at the zero time.
func TestANullStaysNull(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	running, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		Status:    domain.StatusConfirmed, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !running.Running() {
		t.Fatal("the fixture entry is not running, so this test proves nothing")
	}

	var binID int64
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		binID, txErr = CaptureTx(ctx, tx, DeletedRecord{
			Kind: "time_entry", RecordID: running.ID, UserID: user.ID,
			Summary: "a running timer", DeletedBy: user.ID, DeletedAt: time.Now(),
		}, []Capture{{Table: "time_entries", Where: "id = ?", Args: []any{running.ID}}})
		if txErr != nil {
			return txErr
		}
		_, txErr = tx.ExecContext(ctx, `DELETE FROM time_entries WHERE id = ?`, running.ID)
		return txErr
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	record, err := db.GetDeleted(ctx, binID)
	if err != nil {
		t.Fatalf("read bin row: %v", err)
	}
	if err := db.InTx(ctx, func(tx *sql.Tx) error {
		return RestoreTx(ctx, tx, record, []string{"time_entries"})
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	restored, err := db.GetEntry(ctx, running.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !restored.Running() {
		t.Error("a running timer came back stopped; the NULL end time was " +
			"captured as a value")
	}
}

// TestAPayloadThisVersionDoesNotUnderstandIsRefused.
//
// A record deleted by a newer version of the application can hold rows from a
// table this one has never heard of. Writing back the tables it does recognise
// and silently dropping the rest would restore a half-record, which is worse
// than refusing: it looks like it worked.
func TestAPayloadThisVersionDoesNotUnderstandIsRefused(t *testing.T) {
	db, ctx, user, entryID := binFixture(t)

	var binID int64
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		binID, txErr = CaptureTx(ctx, tx, DeletedRecord{
			Kind: "time_entry", RecordID: entryID, UserID: user.ID,
			Summary: "from the future", DeletedBy: user.ID, DeletedAt: time.Now(),
		}, []Capture{{Table: "time_entries", Where: "id = ?", Args: []any{entryID}}})
		return txErr
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	record, err := db.GetDeleted(ctx, binID)
	if err != nil {
		t.Fatalf("read bin row: %v", err)
	}
	// A table the write-back order does not name.
	record.Payload = `{"time_entries": [], "entry_footnotes": [{"id": "1"}]}`

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return RestoreTx(ctx, tx, record, []string{"time_entries", "entry_tags"})
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("restoring an unknown table = %v, want a refusal", err)
	}
	if err != nil && !strings.Contains(err.Error(), "entry_footnotes") {
		t.Errorf("the refusal does not name the table it could not restore: %v", err)
	}
}

// TestUnreadablePayloadsAreRefused.
func TestUnreadablePayloadsAreRefused(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	err := db.InTx(ctx, func(tx *sql.Tx) error {
		return RestoreTx(ctx, tx, DeletedRecord{
			ID: 1, Kind: "time_entry", Payload: "not json at all",
		}, []string{"time_entries"})
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("restoring an unreadable payload = %v, want a refusal", err)
	}

	if _, err := PayloadRows("also not json", "time_entries"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("reading an unreadable payload = %v, want a refusal", err)
	}
}

// TestTheBinIsListedByOwnerAndByInstance.
//
// A record with an owner is that person's; one with no owner - a tag, a
// contract term - belongs to the instance and is listed only for somebody
// entitled to see it, which the service decides before calling here.
func TestTheBinIsListedByOwnerAndByInstance(t *testing.T) {
	db, ctx, user, entryID := binFixture(t)

	other, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Other", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	bin := func(kind string, owner int64, summary string) {
		t.Helper()
		if err := db.InTx(ctx, func(tx *sql.Tx) error {
			_, txErr := CaptureTx(ctx, tx, DeletedRecord{
				Kind: kind, RecordID: entryID, UserID: owner, Summary: summary,
				DeletedBy: user.ID, DeletedAt: time.Now(),
			}, []Capture{{Table: "time_entries", Where: "id = ?", Args: []any{entryID}}})
			return txErr
		}); err != nil {
			t.Fatalf("capture %s: %v", summary, err)
		}
	}
	bin("time_entry", user.ID, "mine")
	bin("time_entry", other.ID, "theirs")
	bin("tag", 0, "the instance's")

	// Without the instance's records.
	mine, err := db.ListDeleted(ctx, user.ID, false, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mine) != 1 || mine[0].Summary != "mine" {
		t.Errorf("listing for one person returned %d records (%v), want only theirs",
			len(mine), summaries(mine))
	}

	// With them.
	withInstance, err := db.ListDeleted(ctx, user.ID, true, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(withInstance) != 2 {
		t.Errorf("listing with the instance's records returned %v, want mine "+
			"and the instance's and never the other person's", summaries(withInstance))
	}
	for _, record := range withInstance {
		if record.Summary == "theirs" {
			t.Error("an administrator's listing includes another person's deletions")
		}
	}

	// And the name of whoever deleted it, which the screen shows.
	if withInstance[0].DeletedByName == "" {
		t.Error("the listing does not say who deleted the record")
	}
}

func summaries(records []DeletedRecord) []string {
	var out []string
	for _, record := range records {
		out = append(out, record.Summary)
	}
	return out
}

// TestAMissingBinRowIsNotFound.
func TestAMissingBinRowIsNotFound(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.GetDeleted(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading a bin row that is not there = %v, want not found", err)
	}
}
