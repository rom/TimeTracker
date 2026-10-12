package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Dated contract terms.
//
// Terms attach to a customer or a project and carry the date they take effect.
// The row that applies to a given day is the latest one on or before it, and a
// project's are merged over its customer's field by field - see
// docs/adr/0026-dated-contract-terms.md.

// termsColumns is the one column list every terms query uses, for the reason
// customerColumns exists: a rule written by the insert and missing from the
// select is a contract term that silently stops applying.
const termsColumns = `id, scope, scope_id, effective_from,
	overtime_rate_minor, overtime_multiplier_pct,
	overtime_daily_threshold_seconds, overtime_weekly_threshold_seconds,
	travel_billing, travel_rate_minor, travel_multiplier_pct,
	expense_markup_pct, expense_billing, mileage_rate_minor, per_diem_minor,
	receipt_required_above_minor, note, created_at, updated_at`

// termsArgs returns the values in the order termsColumns names them.
func termsArgs(t domain.ContractTerms) []any {
	r := t.Rules
	return []any{
		string(t.Scope), t.ScopeID, t.EffectiveFrom,
		r.OvertimeRateMinor, r.OvertimeMultiplierPct,
		r.OvertimeDailyThresholdSeconds, r.OvertimeWeeklyThresholdSeconds,
		string(r.TravelBilling), r.TravelRateMinor, r.TravelMultiplierPct,
		r.ExpenseMarkupPct, string(r.ExpenseBilling), r.MileageRateMinor, r.PerDiemMinor,
		r.ReceiptRequiredAboveMinor, t.Note,
	}
}

// CreateContractTerms inserts one dated set of terms.
func (db *DB) CreateContractTerms(ctx context.Context, t domain.ContractTerms) (domain.ContractTerms, error) {
	return CreateContractTermsTx(ctx, db.write, t)
}

// CreateContractTermsTx inserts one dated set using the given executor.
func CreateContractTermsTx(ctx context.Context, db Execer, t domain.ContractTerms) (domain.ContractTerms, error) {
	now := time.Now()
	args := append(termsArgs(t), formatTime(now), formatTime(now))

	res, err := db.ExecContext(ctx, `
		INSERT INTO contract_terms (scope, scope_id, effective_from,
			overtime_rate_minor, overtime_multiplier_pct,
			overtime_daily_threshold_seconds, overtime_weekly_threshold_seconds,
			travel_billing, travel_rate_minor, travel_multiplier_pct,
			expense_markup_pct, expense_billing, mileage_rate_minor, per_diem_minor,
			receipt_required_above_minor, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	if err != nil {
		return domain.ContractTerms{}, fmt.Errorf("create contract terms: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.ContractTerms{}, err
	}
	t.ID = id
	t.CreatedAt, t.UpdatedAt = now, now
	return t, nil
}

// UpdateContractTerms saves an edited set.
func (db *DB) UpdateContractTerms(ctx context.Context, t domain.ContractTerms) error {
	return UpdateContractTermsTx(ctx, db.write, t)
}

// UpdateContractTermsTx is UpdateContractTerms on the caller's executor.
func UpdateContractTermsTx(ctx context.Context, db Execer, t domain.ContractTerms) error {
	args := append(termsArgs(t), formatTime(time.Now()), t.ID)

	res, err := db.ExecContext(ctx, `
		UPDATE contract_terms SET scope = ?, scope_id = ?, effective_from = ?,
		       overtime_rate_minor = ?, overtime_multiplier_pct = ?,
		       overtime_daily_threshold_seconds = ?, overtime_weekly_threshold_seconds = ?,
		       travel_billing = ?, travel_rate_minor = ?, travel_multiplier_pct = ?,
		       expense_markup_pct = ?, expense_billing = ?, mileage_rate_minor = ?,
		       per_diem_minor = ?, receipt_required_above_minor = ?, note = ?,
		       updated_at = ?
		WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("update contract terms: %w", err)
	}
	return requireOneRow(res)
}

// DeleteContractTerms removes one dated set.
//
// Terms are deleted rather than archived, unlike the catalogue: they are not
// referenced by anything. An entry does not point at the terms that priced it -
// it carries the resulting rate, frozen (ADR-0014) - so removing a set cannot
// orphan history.
func (db *DB) DeleteContractTerms(ctx context.Context, id int64) error {
	return DeleteContractTermsTx(ctx, db.write, id)
}

// DeleteContractTermsTx is DeleteContractTerms on the caller's executor.
func DeleteContractTermsTx(ctx context.Context, db Execer, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM contract_terms WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete contract terms: %w", err)
	}
	return requireOneRow(res)
}

// GetContractTerms loads one set.
func (db *DB) GetContractTerms(ctx context.Context, id int64) (domain.ContractTerms, error) {
	row := db.read.QueryRowContext(ctx,
		`SELECT `+termsColumns+` FROM contract_terms WHERE id = ?`, id)
	return scanTerms(row)
}

// ListContractTerms returns one scope's sets, newest effective date first.
//
// Newest first because that is the order resolution walks them in: the first
// row whose effective date has arrived is the one that applies.
func (db *DB) ListContractTerms(ctx context.Context, scope domain.TermsScope, scopeID int64) ([]domain.ContractTerms, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+termsColumns+` FROM contract_terms
		WHERE scope = ? AND scope_id = ?
		ORDER BY effective_from DESC`, string(scope), scopeID)
	if err != nil {
		return nil, fmt.Errorf("list contract terms: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var all []domain.ContractTerms
	for rows.Next() {
		terms, err := scanTerms(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, terms)
	}
	return all, rows.Err()
}

// ResolveTerms returns the rules in force for a project on a day.
//
// Two indexed reads, both small. It is called once per entry that gets billed,
// so it is deliberately not a join across the whole hierarchy: the caller
// usually has the customer and project to hand already.
func (db *DB) ResolveTerms(ctx context.Context, customerID, projectID int64, day string) (domain.RateRules, error) {
	customerTerms, err := db.ListContractTerms(ctx, domain.TermsForCustomer, customerID)
	if err != nil {
		return domain.RateRules{}, err
	}
	projectTerms, err := db.ListContractTerms(ctx, domain.TermsForProject, projectID)
	if err != nil {
		return domain.RateRules{}, err
	}
	// The merge order lives in the domain, so the billing path and the screen
	// that previews the terms cannot disagree about it.
	return domain.ResolveTerms(customerTerms, projectTerms, day), nil
}

func scanTerms(row rowScanner) (domain.ContractTerms, error) {
	var t domain.ContractTerms
	var scope, travelBilling, expenseBilling, createdAt, updatedAt string

	err := row.Scan(&t.ID, &scope, &t.ScopeID, &t.EffectiveFrom,
		&t.Rules.OvertimeRateMinor, &t.Rules.OvertimeMultiplierPct,
		&t.Rules.OvertimeDailyThresholdSeconds, &t.Rules.OvertimeWeeklyThresholdSeconds,
		&travelBilling, &t.Rules.TravelRateMinor, &t.Rules.TravelMultiplierPct,
		&t.Rules.ExpenseMarkupPct, &expenseBilling, &t.Rules.MileageRateMinor,
		&t.Rules.PerDiemMinor, &t.Rules.ReceiptRequiredAboveMinor, &t.Note,
		&createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContractTerms{}, ErrNotFound
	}
	if err != nil {
		return domain.ContractTerms{}, err
	}

	t.Scope = domain.TermsScope(scope)
	t.Rules.TravelBilling = domain.TravelBilling(travelBilling)
	t.Rules.ExpenseBilling = domain.ExpenseBilling(expenseBilling)
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.ContractTerms{}, err
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.ContractTerms{}, err
	}
	return t, nil
}
