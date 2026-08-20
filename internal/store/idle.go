package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Idle observation storage.
//
// Rows are written by the browser reporting what it saw and are read back as
// questions for a person to answer. They are never deleted when answered: the
// resolution is stored on the row, so "the application saw nothing here and I
// said it was work" survives as a fact about the timesheet rather than
// disappearing the moment it stops being a prompt.

const idleSelect = `
	SELECT o.id, o.entry_id, o.user_id, o.started_at, o.ended_at, o.source,
	       o.resolution, o.resolved_at, o.created_at,
	       a.name, p.name, c.name, a.colour_key, e.note
	FROM idle_observations o
	JOIN time_entries e ON e.id = o.entry_id
	JOIN assignments  a ON a.id = e.assignment_id
	JOIN projects     p ON p.id = a.project_id
	JOIN customers    c ON c.id = p.customer_id`

// CreateIdleObservation records one observed stretch.
func (db *DB) CreateIdleObservation(ctx context.Context, o domain.IdleObservation) (int64, error) {
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO idle_observations
		    (entry_id, user_id, started_at, ended_at, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		o.EntryID, o.UserID, formatTime(o.StartedAt), formatTime(o.EndedAt),
		string(o.Source), formatTime(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("record idle observation: %w", err)
	}
	return res.LastInsertId()
}

// OverlappingIdleObservation finds an unresolved observation of the same entry
// that already covers this stretch.
//
// The page reports a stretch when it notices one and again when the timer stops,
// and a laptop woken twice in a lunch hour reports two overlapping stretches of
// the same absence. Merging them into one prompt is the difference between being
// asked once and being asked repeatedly about the same hour.
//
// It returns the widest overlapping row, or ErrNotFound.
func (db *DB) OverlappingIdleObservation(ctx context.Context, entryID int64, from, to time.Time) (domain.IdleObservation, error) {
	row := db.read.QueryRowContext(ctx, idleSelect+`
		WHERE o.entry_id = ? AND o.resolution = ''
		  AND o.started_at < ? AND o.ended_at > ?
		ORDER BY (julianday(o.ended_at) - julianday(o.started_at)) DESC
		LIMIT 1`,
		entryID, formatTime(to), formatTime(from))
	return scanIdle(row)
}

// WidenIdleObservation extends a stretch to cover a newly reported one.
func (db *DB) WidenIdleObservation(ctx context.Context, id int64, from, to time.Time) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE idle_observations
		SET started_at = MIN(started_at, ?), ended_at = MAX(ended_at, ?)
		WHERE id = ? AND resolution = ''`,
		formatTime(from), formatTime(to), id)
	if err != nil {
		return fmt.Errorf("widen idle observation: %w", err)
	}
	return requireOneRow(res)
}

// GetIdleObservation loads one by id.
func (db *DB) GetIdleObservation(ctx context.Context, id int64) (domain.IdleObservation, error) {
	return scanIdle(db.read.QueryRowContext(ctx, idleSelect+` WHERE o.id = ?`, id))
}

// UnresolvedIdleObservations returns what a person has not decided about yet.
//
// Only stopped entries: a resolution rewrites an interval and a running timer's
// interval is still being measured, so an observation on a running timer is
// shown as a notice and becomes a question when the timer stops.
func (db *DB) UnresolvedIdleObservations(ctx context.Context, userID int64, limit int) ([]domain.IdleObservation, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.read.QueryContext(ctx, idleSelect+`
		WHERE o.user_id = ? AND o.resolution = '' AND e.ended_at IS NOT NULL
		ORDER BY o.started_at DESC
		LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list idle observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.IdleObservation
	for rows.Next() {
		o, err := scanIdle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RunningIdleObservations returns unresolved observations on timers still going,
// for the notice that says so without offering a decision yet.
func (db *DB) RunningIdleObservations(ctx context.Context, userID int64) ([]domain.IdleObservation, error) {
	rows, err := db.read.QueryContext(ctx, idleSelect+`
		WHERE o.user_id = ? AND o.resolution = '' AND e.ended_at IS NULL
		ORDER BY o.started_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list running idle observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.IdleObservation
	for rows.Next() {
		o, err := scanIdle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ResolveIdleObservationTx records a decision.
//
// In a transaction because the decision and the change it causes to the entry
// have to commit together: a discard that trimmed the entry and then failed to
// mark the observation would ask the same question again about time it had
// already removed.
func ResolveIdleObservationTx(ctx context.Context, tx *sql.Tx, id int64, resolution domain.IdleResolution, at time.Time) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE idle_observations SET resolution = ?, resolved_at = ?
		WHERE id = ? AND resolution = ''`,
		string(resolution), formatTime(at), id)
	if err != nil {
		return fmt.Errorf("resolve idle observation: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		// Somebody answered it in another tab between the read and the write.
		return ErrNotFound
	}
	return nil
}

// ResolveIdleObservationsForEntryTx closes the remaining questions about one
// entry.
//
// Used when an entry is deleted or its interval is rewritten by hand: an
// observation of a stretch of an entry that no longer looks like that is not a
// question worth asking, and would be answered against the wrong interval.
func ResolveIdleObservationsForEntryTx(ctx context.Context, tx *sql.Tx, entryID int64, resolution domain.IdleResolution, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE idle_observations SET resolution = ?, resolved_at = ?
		WHERE entry_id = ? AND resolution = ''`,
		string(resolution), formatTime(at), entryID)
	if err != nil {
		return fmt.Errorf("close idle observations: %w", err)
	}
	return nil
}

func scanIdle(row rowScanner) (domain.IdleObservation, error) {
	var o domain.IdleObservation
	var source, resolution, startedAt, endedAt, resolvedAt, createdAt string

	err := row.Scan(&o.ID, &o.EntryID, &o.UserID, &startedAt, &endedAt, &source,
		&resolution, &resolvedAt, &createdAt,
		&o.AssignmentName, &o.ProjectName, &o.CustomerName, &o.ColourKey, &o.EntryNote)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IdleObservation{}, ErrNotFound
	}
	if err != nil {
		return domain.IdleObservation{}, err
	}

	o.Source = domain.IdleSource(source)
	o.Resolution = domain.IdleResolution(resolution)
	for _, field := range []struct {
		raw    string
		target *time.Time
	}{
		{startedAt, &o.StartedAt},
		{endedAt, &o.EndedAt},
		{resolvedAt, &o.ResolvedAt},
		{createdAt, &o.CreatedAt},
	} {
		if field.raw == "" {
			continue
		}
		parsed, perr := parseTime(field.raw)
		if perr != nil {
			return domain.IdleObservation{}, perr
		}
		*field.target = parsed
	}
	return o, nil
}
