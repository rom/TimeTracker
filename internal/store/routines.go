package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Routine storage.

const routineSelect = `
	SELECT r.id, r.user_id, r.assignment_id, r.name, r.note, r.weekdays,
	       r.start_time, r.duration_seconds, r.billable, r.kind, r.tags,
	       r.active, r.sort_order, r.created_at, r.updated_at,
	       a.name, a.colour_key, a.icon, p.name, c.name
	FROM routines r
	JOIN assignments a ON a.id = r.assignment_id
	JOIN projects    p ON p.id = a.project_id
	JOIN customers   c ON c.id = p.customer_id`

// CreateRoutine inserts a template.
func (db *DB) CreateRoutine(ctx context.Context, r domain.Routine) (domain.Routine, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO routines (user_id, assignment_id, name, note, weekdays,
		    start_time, duration_seconds, billable, kind, tags, active,
		    sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.UserID, r.AssignmentID, r.Name, r.Note, domain.FormatWeekdays(r.Weekdays),
		r.StartTime, r.DurationSeconds, boolToInt(r.Billable), string(r.Kind),
		domain.FormatTagList(r.Tags), boolToInt(r.Active), r.SortOrder,
		formatTime(now), formatTime(now))
	if err != nil {
		return domain.Routine{}, fmt.Errorf("create routine: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Routine{}, err
	}
	r.ID = id
	return r, nil
}

// UpdateRoutine saves an edited template.
func (db *DB) UpdateRoutine(ctx context.Context, r domain.Routine) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE routines SET assignment_id = ?, name = ?, note = ?, weekdays = ?,
		       start_time = ?, duration_seconds = ?, billable = ?, kind = ?,
		       tags = ?, active = ?, sort_order = ?, updated_at = ?
		WHERE id = ?`,
		r.AssignmentID, r.Name, r.Note, domain.FormatWeekdays(r.Weekdays),
		r.StartTime, r.DurationSeconds, boolToInt(r.Billable), string(r.Kind),
		domain.FormatTagList(r.Tags), boolToInt(r.Active), r.SortOrder,
		formatTime(time.Now()), r.ID)
	if err != nil {
		return fmt.Errorf("update routine: %w", err)
	}
	return requireOneRow(res)
}

// DeleteRoutine removes a template. Entries it produced are ordinary entries and
// are untouched: a routine is a way of typing, not a record of anything.
func (db *DB) DeleteRoutine(ctx context.Context, id int64) error {
	res, err := db.write.ExecContext(ctx, `DELETE FROM routines WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete routine: %w", err)
	}
	return requireOneRow(res)
}

// GetRoutine loads one.
func (db *DB) GetRoutine(ctx context.Context, id int64) (domain.Routine, error) {
	row := db.read.QueryRowContext(ctx, routineSelect+` WHERE r.id = ?`, id)
	return scanRoutine(row)
}

// ListRoutines returns a person's templates, in the order they chose.
func (db *DB) ListRoutines(ctx context.Context, userID int64, activeOnly bool) ([]domain.Routine, error) {
	conditions := []string{`r.user_id = ?`}
	args := []any{userID}
	if activeOnly {
		conditions = append(conditions, `r.active = 1`)
	}

	rows, err := db.read.QueryContext(ctx,
		routineSelect+whereClause(conditions)+` ORDER BY r.sort_order, r.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, fmt.Errorf("list routines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var routines []domain.Routine
	for rows.Next() {
		routine, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		routines = append(routines, routine)
	}
	return routines, rows.Err()
}

func scanRoutine(row rowScanner) (domain.Routine, error) {
	var r domain.Routine
	var weekdays, tags, kind, createdAt, updatedAt string
	var billable, active int

	err := row.Scan(&r.ID, &r.UserID, &r.AssignmentID, &r.Name, &r.Note, &weekdays,
		&r.StartTime, &r.DurationSeconds, &billable, &kind, &tags,
		&active, &r.SortOrder, &createdAt, &updatedAt,
		&r.AssignmentName, &r.ColourKey, &r.Icon, &r.ProjectName, &r.CustomerName)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Routine{}, ErrNotFound
	}
	if err != nil {
		return domain.Routine{}, err
	}

	r.Weekdays = domain.ParseWeekdays(weekdays)
	r.Tags = domain.ParseTagList(tags)
	r.Kind = domain.EntryKind(kind)
	r.Billable = billable != 0
	r.Active = active != 0
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Routine{}, err
	}
	if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Routine{}, err
	}
	return r, nil
}

// QuickStartAssignments returns what somebody should be able to start in one
// click.
//
// Ordered by favourite first, then by how often they have been used lately,
// then by how recently. Frequency before recency is deliberate: the assignment
// somebody touched once yesterday is less likely to be wanted than the one they
// work on every day, and a list that reshuffles itself after every entry is one
// nobody can build muscle memory for.
//
// The window is recent rather than all-time so that finished work drops off
// without anybody archiving it.
func (db *DB) QuickStartAssignments(ctx context.Context, userID int64, since time.Time, limit int) ([]domain.Assignment, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := db.read.QueryContext(ctx, `
		SELECT a.id, a.project_id, a.name, a.code, a.colour_key, a.icon, a.billable_default,
		       a.rate_minor, a.sort_order, a.favourite, a.archived_at, a.created_at,
		       p.name, p.customer_id, c.name, c.currency
		FROM assignments a
		JOIN projects  p ON p.id = a.project_id
		JOIN customers c ON c.id = p.customer_id
		LEFT JOIN (
			SELECT assignment_id, COUNT(*) AS uses, MAX(started_at) AS last_used
			FROM time_entries
			WHERE user_id = ? AND started_at >= ?
			GROUP BY assignment_id
		) recent ON recent.assignment_id = a.id
		WHERE a.archived_at IS NULL
		  AND (a.favourite = 1 OR recent.uses IS NOT NULL)
		ORDER BY a.favourite DESC,
		         COALESCE(recent.uses, 0) DESC,
		         COALESCE(recent.last_used, '') DESC,
		         a.sort_order, a.name COLLATE NOCASE
		LIMIT ?`, userID, formatTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("quick-start assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var assignments []domain.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}
