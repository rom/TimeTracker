package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/rom/timetracker/internal/domain"
)

// Billing run storage.
//
// Two things happen together whenever a run is made or unmade: the run row is
// written and the entries are stamped with it, or released from it. Both are
// transactional for the obvious reason - a run whose entries were not stamped
// would be an invoice for time the application still thinks is unbilled, and
// entries stamped with a run that was not written would be unbillable forever.

const billingRunSelect = `
	SELECT r.id, r.customer_id, r.starts_on, r.ends_on, r.reference, r.note,
	       r.currency, r.entry_count, r.billable_seconds, r.amount_minor,
	       r.created_by, r.created_at, r.voided_at, r.voided_by,
	       COALESCE(c.name, '')
	FROM billing_runs r
	LEFT JOIN customers c ON c.id = r.customer_id`

// CreateBillingRunTx writes the run on the caller's transaction.
func CreateBillingRunTx(ctx context.Context, db Execer, r domain.BillingRun) (domain.BillingRun, error) {
	result, err := db.ExecContext(ctx, `
		INSERT INTO billing_runs (customer_id, starts_on, ends_on, reference, note,
		                          currency, entry_count, billable_seconds, amount_minor,
		                          created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.CustomerID, r.StartsOn, r.EndsOn, r.Reference, r.Note,
		r.Currency, r.EntryCount, r.BillableSeconds, r.AmountMinor,
		r.CreatedBy, formatTime(r.CreatedAt))
	if err != nil {
		return domain.BillingRun{}, fmt.Errorf("create billing run: %w", err)
	}
	if r.ID, err = result.LastInsertId(); err != nil {
		return domain.BillingRun{}, err
	}
	return r, nil
}

// StampEntriesTx marks entries as belonging to a run.
//
// The WHERE clause repeats the condition that chose them - unbilled, and this
// customer's - rather than trusting the ids it was handed. Between the moment
// the candidates were read and the moment they are stamped, another person's
// run could have claimed one; the repeated condition is what makes that a row
// that does not update rather than an hour billed twice. The caller compares
// the count.
func StampEntriesTx(ctx context.Context, db Execer, runID int64, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := `
		UPDATE time_entries SET billing_run_id = ?
		WHERE billing_run_id = 0 AND id IN (` + marks + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, runID)
	for _, id := range ids {
		args = append(args, id)
	}
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("stamp entries: %w", err)
	}
	return result.RowsAffected()
}

// VoidBillingRunTx cancels a run and releases its entries.
func VoidBillingRunTx(ctx context.Context, db Execer, runID, voidedBy int64, at string) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE billing_runs SET voided_at = ?, voided_by = ?
		WHERE id = ? AND voided_at = ''`, at, voidedBy, runID); err != nil {
		return fmt.Errorf("void billing run: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE time_entries SET billing_run_id = 0 WHERE billing_run_id = ?`,
		runID); err != nil {
		return fmt.Errorf("release billed entries: %w", err)
	}
	return nil
}

// GetBillingRun reads one.
func (db *DB) GetBillingRun(ctx context.Context, id int64) (domain.BillingRun, error) {
	return scanBillingRun(db.read.QueryRowContext(ctx, billingRunSelect+` WHERE r.id = ?`, id))
}

// ListBillingRuns returns runs, newest first, optionally for one customer.
func (db *DB) ListBillingRuns(ctx context.Context, customerID int64, limit int) ([]domain.BillingRun, error) {
	query := billingRunSelect
	var args []any
	if customerID != 0 {
		query += ` WHERE r.customer_id = ?`
		args = append(args, customerID)
	}
	query += ` ORDER BY r.created_at DESC, r.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list billing runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var runs []domain.BillingRun
	for rows.Next() {
		run, scanErr := scanBillingRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// scanBillingRun reads one row.
func scanBillingRun(row rowScanner) (domain.BillingRun, error) {
	var r domain.BillingRun
	var createdAt, voidedAt string
	err := row.Scan(&r.ID, &r.CustomerID, &r.StartsOn, &r.EndsOn, &r.Reference,
		&r.Note, &r.Currency, &r.EntryCount, &r.BillableSeconds, &r.AmountMinor,
		&r.CreatedBy, &createdAt, &voidedAt, &r.VoidedBy, &r.CustomerName)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BillingRun{}, ErrNotFound
	}
	if err != nil {
		return domain.BillingRun{}, err
	}
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.BillingRun{}, err
	}
	// Empty is the usual case - most runs are not voided - and parseTime would
	// call it an error rather than "no".
	if voidedAt != "" {
		if r.VoidedAt, err = parseTime(voidedAt); err != nil {
			return domain.BillingRun{}, err
		}
	}
	return r, nil
}
