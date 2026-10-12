package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Budget consumption.
//
// One query for the totals and one for the recent window, both grouped by
// project, rather than a query per project: a burn report over fifty projects
// should cost two round trips, not a hundred.

// ProjectConsumption is what one project has used.
type ProjectConsumption struct {
	ProjectID  int64
	Seconds    int64
	Minor      int64
	FirstEntry time.Time
	LastEntry  time.Time
}

// ConsumptionByProject totals the counting entries of every project in scope.
//
// "Counting" is the same rule every total in this application uses: confirmed
// and not flagged. A proposal nobody has accepted and an entry marked for review
// are not consumption - they are questions - and a budget report that included
// them would show a project over its cap because somebody typed the wrong hours
// and flagged it.
//
// Billable seconds are deliberately not what is summed for the hours figure.
// Hours against an hours budget are hours somebody worked, whoever ends up
// paying for them. Money is the billed amount, which only billable work
// contributes to.
func (db *DB) ConsumptionByProject(ctx context.Context, scope Scope, from, to time.Time) (map[int64]ProjectConsumption, error) {
	conditions := []string{`e.status = 'confirmed'`, `e.flagged = 0`, `e.ended_at IS NOT NULL`}
	var args []any

	if scoped, scopeArgs := scope.condition("a.project_id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}
	// The bounds are compared against the bare column with the arithmetic on the
	// other side, so an index can answer them (ADR-0032).
	if !from.IsZero() {
		conditions = append(conditions, `e.started_at >= ?`)
		args = append(args, formatTime(from))
	}
	if !to.IsZero() {
		conditions = append(conditions, `e.started_at < ?`)
		args = append(args, formatTime(to))
	}

	rows, err := db.read.QueryContext(ctx, `
		SELECT a.project_id,
		       SUM(e.duration_seconds),
		       SUM(e.amount_minor),
		       MIN(e.started_at),
		       MAX(e.started_at)
		FROM time_entries e
		JOIN assignments a ON a.id = e.assignment_id
		JOIN projects    p ON p.id = a.project_id`+
		whereClause(conditions)+`
		GROUP BY a.project_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("total consumption: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int64]ProjectConsumption{}
	for rows.Next() {
		var c ProjectConsumption
		var seconds, minor sql.NullInt64
		var first, last sql.NullString
		if err := rows.Scan(&c.ProjectID, &seconds, &minor, &first, &last); err != nil {
			return nil, err
		}
		c.Seconds, c.Minor = seconds.Int64, minor.Int64
		for _, field := range []struct {
			raw    sql.NullString
			target *time.Time
		}{{first, &c.FirstEntry}, {last, &c.LastEntry}} {
			if !field.raw.Valid || field.raw.String == "" {
				continue
			}
			parsed, perr := parseTime(field.raw.String)
			if perr != nil {
				return nil, perr
			}
			*field.target = parsed
		}
		out[c.ProjectID] = c
	}
	return out, rows.Err()
}

// ActiveWeeksByProject counts the distinct weeks each project had any counting
// entry in, inside a range.
//
// The denominator of the burn rate. Counted in SQL rather than by walking the
// entries, because the alternative is loading a window of every project's
// entries to divide by a small integer.
//
// The week is derived with strftime('%Y-%W'), which is a *grouping* key rather
// than a date: two entries in the same week share it and entries in different
// weeks do not, which is all the count needs. It deliberately does not agree
// with the configured week start - a burn rate averaged over "four weeks" does
// not become a different number because an instance starts its weeks on Sunday,
// and paying an index scan to make it would buy nothing a reader could see.
func (db *DB) ActiveWeeksByProject(ctx context.Context, scope Scope, from, to time.Time) (map[int64]int, error) {
	conditions := []string{`e.status = 'confirmed'`, `e.flagged = 0`, `e.ended_at IS NOT NULL`}
	var args []any

	if scoped, scopeArgs := scope.condition("a.project_id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}
	if !from.IsZero() {
		conditions = append(conditions, `e.started_at >= ?`)
		args = append(args, formatTime(from))
	}
	if !to.IsZero() {
		conditions = append(conditions, `e.started_at < ?`)
		args = append(args, formatTime(to))
	}

	rows, err := db.read.QueryContext(ctx, `
		SELECT project_id, COUNT(*) FROM (
			SELECT a.project_id AS project_id,
			       strftime('%Y-%W', e.started_at) AS week
			FROM time_entries e
			JOIN assignments a ON a.id = e.assignment_id
			JOIN projects    p ON p.id = a.project_id`+
		whereClause(conditions)+`
			GROUP BY a.project_id, week)
		GROUP BY project_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("count active weeks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int64]int{}
	for rows.Next() {
		var projectID int64
		var weeks int
		if err := rows.Scan(&projectID, &weeks); err != nil {
			return nil, err
		}
		out[projectID] = weeks
	}
	return out, rows.Err()
}

// BudgetedProjects lists the projects in scope that have a budget set.
//
// Archived projects are included: a project archived over its budget is exactly
// the row somebody wants when they ask what went wrong, and the report can mark
// it rather than hide it.
func (db *DB) BudgetedProjects(ctx context.Context, scope Scope) ([]domain.Project, error) {
	conditions := []string{`(p.budget_seconds > 0 OR p.budget_minor > 0)`}
	var args []any
	if scoped, scopeArgs := scope.condition("p.id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}

	rows, err := db.read.QueryContext(ctx, projectSelect+whereClause(conditions)+
		` ORDER BY c.name COLLATE NOCASE, p.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, fmt.Errorf("list budgeted projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var projects []domain.Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}
