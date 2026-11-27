package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rom/timetracker/internal/domain"
)

// Absence storage.
//
// The only question asked of this table is "what covers this range, for this
// person or for everybody", so that is the only query here. A public holiday is
// stored against user 0 and comes back for everyone, which is the same
// convention the recycle bin uses for records belonging to the instance.

const absenceSelect = `
	SELECT a.id, a.user_id, a.kind, a.starts_on, a.ends_on, a.note,
	       a.created_by, a.created_at,
	       COALESCE(u.display_name, '')
	FROM absences a
	LEFT JOIN users u ON u.id = a.user_id`

// CreateAbsenceTx records one on the caller's transaction.
func CreateAbsenceTx(ctx context.Context, db Execer, a domain.Absence) (domain.Absence, error) {
	result, err := db.ExecContext(ctx, `
		INSERT INTO absences (user_id, kind, starts_on, ends_on, note, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, string(a.Kind), a.StartsOn, a.EndsOn, a.Note,
		a.CreatedBy, formatTime(a.CreatedAt))
	if err != nil {
		return domain.Absence{}, fmt.Errorf("create absence: %w", err)
	}
	if a.ID, err = result.LastInsertId(); err != nil {
		return domain.Absence{}, err
	}
	return a, nil
}

// ListAbsences returns everything covering a range for one person, including
// the public holidays that apply to everybody.
//
// The overlap test is the awkward half: an absence covers the range when it
// starts before the range ends and ends after the range begins. Written the
// other way round - starts inside, or ends inside - a fortnight's holiday
// spanning the whole of a one-week query would not be found at all.
func (db *DB) ListAbsences(ctx context.Context, userID int64, from, to string) ([]domain.Absence, error) {
	rows, err := db.read.QueryContext(ctx, absenceSelect+`
		WHERE (a.user_id = ? OR a.user_id = 0)
		  AND a.starts_on <= ? AND a.ends_on >= ?
		ORDER BY a.starts_on, a.id`, userID, to, from)
	if err != nil {
		return nil, fmt.Errorf("list absences: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanAbsences(rows)
}

// ListAbsencesForEveryone returns every person's, for an administrator's
// listing. Public holidays are included, as user 0.
func (db *DB) ListAbsencesForEveryone(ctx context.Context, from, to string) ([]domain.Absence, error) {
	rows, err := db.read.QueryContext(ctx, absenceSelect+`
		WHERE a.starts_on <= ? AND a.ends_on >= ?
		ORDER BY a.starts_on, a.id`, to, from)
	if err != nil {
		return nil, fmt.Errorf("list absences: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanAbsences(rows)
}

// GetAbsence loads one.
func (db *DB) GetAbsence(ctx context.Context, id int64) (domain.Absence, error) {
	row := db.read.QueryRowContext(ctx, absenceSelect+` WHERE a.id = ?`, id)
	absence, err := scanAbsence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Absence{}, ErrNotFound
	}
	return absence, err
}

func scanAbsences(rows *sql.Rows) ([]domain.Absence, error) {
	var found []domain.Absence
	for rows.Next() {
		absence, err := scanAbsence(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, absence)
	}
	return found, rows.Err()
}

func scanAbsence(row rowScanner) (domain.Absence, error) {
	var a domain.Absence
	var kind, createdAt string
	if err := row.Scan(&a.ID, &a.UserID, &kind, &a.StartsOn, &a.EndsOn, &a.Note,
		&a.CreatedBy, &createdAt, &a.UserName); err != nil {
		return domain.Absence{}, err
	}
	a.Kind = domain.AbsenceKind(kind)
	var err error
	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Absence{}, err
	}
	return a, nil
}
