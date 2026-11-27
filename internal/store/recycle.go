package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The recycle bin.
//
// Rows are captured whole on the way out and written back whole on the way in,
// by column name, with no per-table serialiser anywhere. Six serialisers for
// six schemas would be six things to keep in step with every future migration,
// and the failure mode is silent: a column added later is simply dropped on
// restore, and nobody finds out until a restored entry is missing a field
// nobody thought to check.
//
// Values are carried as strings or nulls. Every column in the tables this
// covers has TEXT or INTEGER affinity and none holds a blob - attachment bytes
// live in the blob store, not in a row - so a string round-trip is lossless:
// SQLite applies the column's affinity on the way back in, turning "3600" into
// the integer 3600 in an INTEGER column. Numbers through JSON would not be:
// encoding/json decodes every number into a float64, which silently rounds an
// id past 2^53 and turns 3600 into 3600.0.

// DeletedRecord is one thing in the bin.
type DeletedRecord struct {
	ID        int64
	Kind      string
	RecordID  int64
	UserID    int64
	Summary   string
	DeletedBy int64
	DeletedAt time.Time
	// Payload is the captured rows, keyed by table name. Only read on restore.
	Payload string

	// Denormalised for the screen.
	DeletedByName string
}

// CaptureTx copies rows into the bin and returns the bin row's id.
//
// The caller passes the tables to capture in the order they must be written
// back: a parent before the children that reference it. Nothing is deleted
// here; the caller deletes, in the same transaction, so a capture that failed
// cannot leave a record destroyed and unrecoverable.
func CaptureTx(ctx context.Context, tx *sql.Tx, in DeletedRecord, captures []Capture) (int64, error) {
	payload := map[string][]map[string]*string{}
	for _, capture := range captures {
		rows, err := captureRows(ctx, tx, capture)
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			continue
		}
		payload[capture.Table] = append(payload[capture.Table], rows...)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode deleted record: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO deleted_records (kind, record_id, user_id, summary, payload,
		                             deleted_by, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.Kind, in.RecordID, in.UserID, in.Summary, string(encoded),
		in.DeletedBy, formatTime(in.DeletedAt))
	if err != nil {
		return 0, fmt.Errorf("record deletion: %w", err)
	}
	return result.LastInsertId()
}

// Capture names rows to copy: everything in Table matching Where.
type Capture struct {
	Table string
	Where string
	Args  []any
}

// captureRows reads whole rows without naming their columns.
func captureRows(ctx context.Context, tx *sql.Tx, capture Capture) ([]map[string]*string, error) {
	// The table name is never user input - every call site passes a literal -
	// so it is interpolated, which is the only way to write SELECT * FROM a
	// variable table. The WHERE clause's values are bound normally.
	query := fmt.Sprintf(`SELECT * FROM %s WHERE %s`, capture.Table, capture.Where)
	rows, err := tx.QueryContext(ctx, query, capture.Args...)
	if err != nil {
		return nil, fmt.Errorf("capture %s: %w", capture.Table, err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("capture %s: %w", capture.Table, err)
	}

	var captured []map[string]*string
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			return nil, fmt.Errorf("capture %s: %w", capture.Table, err)
		}
		row := make(map[string]*string, len(columns))
		for i, name := range columns {
			cell := cells[i].(*sql.NullString)
			if cell.Valid {
				value := cell.String
				row[name] = &value
				continue
			}
			row[name] = nil
		}
		captured = append(captured, row)
	}
	return captured, rows.Err()
}

// RestoreTx writes a bin row's captured rows back and removes the bin row.
//
// Tables are written in the order given by order, so a parent lands before the
// children that reference it. A table in the payload that order does not name
// is a payload written by a newer version of the application than this one, and
// is refused rather than half-restored.
func RestoreTx(ctx context.Context, tx *sql.Tx, record DeletedRecord, order []string) error {
	var payload map[string][]map[string]*string
	if err := json.Unmarshal([]byte(record.Payload), &payload); err != nil {
		return fmt.Errorf("%w: this deleted record cannot be read back", domain.ErrValidation)
	}

	known := map[string]bool{}
	for _, table := range order {
		known[table] = true
	}
	var unknown []string
	for table := range payload {
		if !known[table] {
			unknown = append(unknown, table)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%w: this record was deleted by a newer version and "+
			"holds rows this one does not know how to restore (%s)",
			domain.ErrValidation, strings.Join(unknown, ", "))
	}

	for _, table := range order {
		for _, row := range payload[table] {
			if err := insertRow(ctx, tx, table, row); err != nil {
				return err
			}
		}
	}

	_, err := tx.ExecContext(ctx, `DELETE FROM deleted_records WHERE id = ?`, record.ID)
	return err
}

// insertRow writes one captured row back under the columns it was captured by.
func insertRow(ctx context.Context, tx *sql.Tx, table string, row map[string]*string) error {
	// Sorted so the statement is stable between runs, which matters only for
	// reading a query log but costs nothing.
	columns := make([]string, 0, len(row))
	for name := range row {
		columns = append(columns, name)
	}
	sort.Strings(columns)

	placeholders := make([]string, len(columns))
	values := make([]any, len(columns))
	for i, name := range columns {
		placeholders[i] = "?"
		if row[name] == nil {
			values[i] = nil
			continue
		}
		values[i] = *row[name]
	}

	query := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	if _, err := tx.ExecContext(ctx, query, values...); err != nil {
		return fmt.Errorf("restore into %s: %w", table, err)
	}
	return nil
}

// ListDeleted returns what is in the bin, newest first.
//
// Scoped like everything else: a person sees what was theirs, and a record that
// belongs to the instance rather than to a person - a tag - is shown to whoever
// may administer the instance, which the service decides before calling here.
func (db *DB) ListDeleted(ctx context.Context, userID int64, includeInstance bool, limit int) ([]DeletedRecord, error) {
	where := `d.user_id = ?`
	args := []any{userID}
	if includeInstance {
		where = `(d.user_id = ? OR d.user_id = 0)`
	}
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)

	rows, err := db.read.QueryContext(ctx, `
		SELECT d.id, d.kind, d.record_id, d.user_id, d.summary, d.deleted_by,
		       d.deleted_at, u.display_name
		FROM deleted_records d
		JOIN users u ON u.id = d.deleted_by
		WHERE `+where+`
		ORDER BY d.deleted_at DESC, d.id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list deleted: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var found []DeletedRecord
	for rows.Next() {
		var record DeletedRecord
		var deletedAt string
		if err := rows.Scan(&record.ID, &record.Kind, &record.RecordID, &record.UserID,
			&record.Summary, &record.DeletedBy, &deletedAt, &record.DeletedByName); err != nil {
			return nil, err
		}
		if record.DeletedAt, err = parseTime(deletedAt); err != nil {
			return nil, err
		}
		found = append(found, record)
	}
	return found, rows.Err()
}

// GetDeleted reads one bin row, payload included.
func (db *DB) GetDeleted(ctx context.Context, id int64) (DeletedRecord, error) {
	var record DeletedRecord
	var deletedAt string
	err := db.read.QueryRowContext(ctx, `
		SELECT id, kind, record_id, user_id, summary, payload, deleted_by, deleted_at
		FROM deleted_records WHERE id = ?`, id).
		Scan(&record.ID, &record.Kind, &record.RecordID, &record.UserID,
			&record.Summary, &record.Payload, &record.DeletedBy, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeletedRecord{}, ErrNotFound
	}
	if err != nil {
		return DeletedRecord{}, err
	}
	if record.DeletedAt, err = parseTime(deletedAt); err != nil {
		return DeletedRecord{}, err
	}
	return record, nil
}

// PayloadRows decodes one table's captured rows out of a payload.
//
// For the few decisions that have to be made about a record before it is put
// back - whether the week it lands in has since been locked, say - which need
// one field rather than the whole capture.
func PayloadRows(payload, table string) ([]map[string]*string, error) {
	var decoded map[string][]map[string]*string
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return nil, fmt.Errorf("%w: this deleted record cannot be read back",
			domain.ErrValidation)
	}
	return decoded[table], nil
}
