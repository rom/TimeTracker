package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Timesheet period storage.
//
// A week with no row is open. Rows appear only once something has happened to a
// week, which keeps the table proportional to activity rather than to the
// calendar - an instance running for five years has no rows for the weeks
// nobody ever submitted.

const periodSelect = `
	SELECT p.id, p.user_id, p.week_start, p.status, p.submitted_at, p.decided_by,
	       p.decided_at, p.note, p.submitted_seconds, p.created_at, p.updated_at,
	       u.display_name
	FROM timesheet_periods p
	JOIN users u ON u.id = p.user_id`

// GetPeriod loads one person's week.
//
// A missing row is not an error: it means the week is open, which is the normal
// state of most weeks. The caller gets a zero-valued period with status open.
func (db *DB) GetPeriod(ctx context.Context, userID int64, weekStart string) (domain.TimesheetPeriod, error) {
	row := db.read.QueryRowContext(ctx,
		periodSelect+` WHERE p.user_id = ? AND p.week_start = ?`, userID, weekStart)

	period, err := scanPeriod(row)
	if errors.Is(err, ErrNotFound) {
		return domain.TimesheetPeriod{
			UserID: userID, WeekStart: weekStart, Status: domain.PeriodOpen,
		}, nil
	}
	return period, err
}

// UpsertPeriod creates or updates a week's record.
//
// The unique index on (user_id, week_start) is what makes this idempotent:
// submitting twice updates one row rather than creating a second.
func (db *DB) UpsertPeriod(ctx context.Context, p domain.TimesheetPeriod) error {
	now := formatTime(time.Now())

	var submittedAt, decidedAt string
	if !p.SubmittedAt.IsZero() {
		submittedAt = formatTime(p.SubmittedAt)
	}
	if !p.DecidedAt.IsZero() {
		decidedAt = formatTime(p.DecidedAt)
	}

	_, err := db.write.ExecContext(ctx, `
		INSERT INTO timesheet_periods (user_id, week_start, status, submitted_at,
		    decided_by, decided_at, note, submitted_seconds, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, week_start) DO UPDATE SET
		    status = excluded.status,
		    submitted_at = excluded.submitted_at,
		    decided_by = excluded.decided_by,
		    decided_at = excluded.decided_at,
		    note = excluded.note,
		    submitted_seconds = excluded.submitted_seconds,
		    updated_at = excluded.updated_at`,
		p.UserID, p.WeekStart, string(p.Status), submittedAt, p.DecidedBy, decidedAt,
		p.Note, p.SubmittedSeconds, now, now)
	if err != nil {
		return fmt.Errorf("save timesheet period: %w", err)
	}
	return nil
}

// ListPeriodsForUser returns a person's recent weeks, newest first.
func (db *DB) ListPeriodsForUser(ctx context.Context, userID int64, limit int) ([]domain.TimesheetPeriod, error) {
	if limit <= 0 {
		limit = 26
	}
	rows, err := db.read.QueryContext(ctx,
		periodSelect+` WHERE p.user_id = ? ORDER BY p.week_start DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list timesheet periods: %w", err)
	}
	return collectPeriods(rows)
}

// ListPeriodsByStatus returns everyone's weeks in a given state, scoped.
//
// This is the approval queue. The scope restricts it to the projects the actor
// is responsible for, so a manager sees their team's submissions and not the
// whole company's.
func (db *DB) ListPeriodsByStatus(ctx context.Context, status domain.PeriodStatus, scope Scope) ([]domain.TimesheetPeriod, error) {
	conditions := []string{`p.status = ?`}
	args := []any{string(status)}

	if !scope.Unrestricted {
		// A period belongs to whoever recorded time in it, so the scope is
		// applied through the entries that week contains. A manager sees a
		// submission when it includes work on one of their projects.
		//
		// Someone who submitted an empty week is deliberately not in the queue:
		// there is nothing to approve.
		if len(scope.ProjectIDs) == 0 && scope.CustomerID == 0 {
			return nil, nil
		}
		placeholders := ""
		if len(scope.ProjectIDs) > 0 {
			placeholders = repeatPlaceholders(len(scope.ProjectIDs))
			conditions = append(conditions, `EXISTS (
				SELECT 1 FROM time_entries e
				JOIN assignments a ON a.id = e.assignment_id
				WHERE e.user_id = p.user_id
				  AND a.project_id IN (`+placeholders+`)
				  AND date(e.started_at) >= date(p.week_start)
				  AND date(e.started_at) < date(p.week_start, '+7 days'))`)
			for _, id := range scope.ProjectIDs {
				args = append(args, id)
			}
		} else {
			conditions = append(conditions, `EXISTS (
				SELECT 1 FROM time_entries e
				JOIN assignments a ON a.id = e.assignment_id
				JOIN projects   pr ON pr.id = a.project_id
				WHERE e.user_id = p.user_id
				  AND pr.customer_id = ?
				  AND date(e.started_at) >= date(p.week_start)
				  AND date(e.started_at) < date(p.week_start, '+7 days'))`)
			args = append(args, scope.CustomerID)
		}
	}

	rows, err := db.read.QueryContext(ctx,
		periodSelect+whereClause(conditions)+` ORDER BY p.week_start DESC, u.display_name`, args...)
	if err != nil {
		return nil, fmt.Errorf("list submitted timesheets: %w", err)
	}
	return collectPeriods(rows)
}

// WeekSeconds totals the counting entries in one person's week.
//
// Used both when submitting - to record what was submitted - and when showing
// an approver the week as it stands now.
func (db *DB) WeekSeconds(ctx context.Context, userID int64, weekStart string) (int64, error) {
	var total sql.NullInt64
	err := db.read.QueryRowContext(ctx, `
		SELECT SUM(duration_seconds) FROM time_entries
		WHERE user_id = ?
		  AND status = 'confirmed'
		  AND flagged = 0
		  AND date(started_at) >= date(?)
		  AND date(started_at) < date(?, '+7 days')`,
		userID, weekStart, weekStart).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("total week: %w", err)
	}
	return total.Int64, nil
}

func collectPeriods(rows *sql.Rows) ([]domain.TimesheetPeriod, error) {
	defer func() { _ = rows.Close() }()

	var periods []domain.TimesheetPeriod
	for rows.Next() {
		period, err := scanPeriod(rows)
		if err != nil {
			return nil, err
		}
		periods = append(periods, period)
	}
	return periods, rows.Err()
}

func scanPeriod(row rowScanner) (domain.TimesheetPeriod, error) {
	var p domain.TimesheetPeriod
	var status, submittedAt, decidedAt, createdAt, updatedAt string

	err := row.Scan(&p.ID, &p.UserID, &p.WeekStart, &status, &submittedAt, &p.DecidedBy,
		&decidedAt, &p.Note, &p.SubmittedSeconds, &createdAt, &updatedAt, &p.UserName)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TimesheetPeriod{}, ErrNotFound
	}
	if err != nil {
		return domain.TimesheetPeriod{}, err
	}

	p.Status = domain.PeriodStatus(status)
	for _, field := range []struct {
		raw    string
		target *time.Time
	}{
		{submittedAt, &p.SubmittedAt},
		{decidedAt, &p.DecidedAt},
		{createdAt, &p.CreatedAt},
		{updatedAt, &p.UpdatedAt},
	} {
		if field.raw == "" {
			continue
		}
		parsed, perr := parseTime(field.raw)
		if perr != nil {
			return domain.TimesheetPeriod{}, perr
		}
		*field.target = parsed
	}
	return p, nil
}

// repeatPlaceholders builds "?,?,?" for an IN clause of n bound values.
func repeatPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*2-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}
