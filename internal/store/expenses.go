package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// expenseSelect is the one place the expense column list lives.
const expenseSelect = `
	SELECT e.id, e.user_id, e.entered_by, e.project_id, e.spent_on, e.category,
	       e.description, e.amount_minor, e.currency, e.billable, e.reimbursable,
	       e.markup_pct, e.billed_minor, e.quantity_milli, e.unit, e.unit_rate_minor,
	       e.status, e.created_at, e.updated_at,
	       p.name, c.id, c.colour_key, c.name, u.display_name, eb.display_name,
	       (SELECT COUNT(*) FROM attachments a
	         WHERE a.owner_type = 'expense' AND a.owner_id = e.id)
	FROM expenses e
	JOIN projects  p  ON p.id  = e.project_id
	JOIN customers c  ON c.id  = p.customer_id
	JOIN users     u  ON u.id  = e.user_id
	JOIN users     eb ON eb.id = e.entered_by`

// CreateExpense inserts an expense.
func (db *DB) CreateExpense(ctx context.Context, e domain.Expense) (domain.Expense, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO expenses (user_id, entered_by, project_id, spent_on, category,
		    description, amount_minor, currency, billable, reimbursable, markup_pct,
		    billed_minor, quantity_milli, unit, unit_rate_minor,
		    status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, e.EnteredBy, e.ProjectID, e.SpentOn, e.Category, e.Description,
		e.AmountMinor, e.Currency, boolToInt(e.Billable), boolToInt(e.Reimbursable),
		e.MarkupPercent, e.BilledMinor,
		e.QuantityMilli, string(e.Unit), e.UnitRateMinor,
		string(e.Status), formatTime(now), formatTime(now))
	if err != nil {
		return domain.Expense{}, fmt.Errorf("create expense: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Expense{}, err
	}
	e.ID = id
	return e, nil
}

// UpdateExpense saves an edited expense.
func (db *DB) UpdateExpense(ctx context.Context, e domain.Expense) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE expenses SET project_id = ?, spent_on = ?, category = ?, description = ?,
		       amount_minor = ?, currency = ?, billable = ?, reimbursable = ?,
		       markup_pct = ?, billed_minor = ?,
		       quantity_milli = ?, unit = ?, unit_rate_minor = ?,
		       status = ?, updated_at = ?
		WHERE id = ?`,
		e.ProjectID, e.SpentOn, e.Category, e.Description, e.AmountMinor, e.Currency,
		boolToInt(e.Billable), boolToInt(e.Reimbursable), e.MarkupPercent, e.BilledMinor,
		e.QuantityMilli, string(e.Unit), e.UnitRateMinor,
		string(e.Status), formatTime(time.Now()), e.ID)
	if err != nil {
		return fmt.Errorf("update expense: %w", err)
	}
	return requireOneRow(res)
}

// DeleteExpense removes an expense and any attachment references it owns.
func (db *DB) DeleteExpense(ctx context.Context, id int64) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM attachments WHERE owner_type = 'expense' AND owner_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM expenses WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete expense: %w", err)
		}
		return requireOneRow(res)
	})
}

// GetExpense loads one expense.
func (db *DB) GetExpense(ctx context.Context, id int64) (domain.Expense, error) {
	row := db.read.QueryRowContext(ctx, expenseSelect+` WHERE e.id = ?`, id)
	return scanExpense(row)
}

// ExpenseFilter narrows a query over expenses.
type ExpenseFilter struct {
	UserID     int64
	From       string // inclusive, YYYY-MM-DD
	To         string // exclusive
	ProjectID  int64
	CustomerID int64
	Scope      Scope
	Statuses   []domain.EntryStatus
	Limit      int
}

// ListExpenses returns expenses matching a filter, newest first.
func (db *DB) ListExpenses(ctx context.Context, f ExpenseFilter) ([]domain.Expense, error) {
	var conditions []string
	var args []any

	if f.UserID != 0 {
		conditions = append(conditions, `e.user_id = ?`)
		args = append(args, f.UserID)
	}
	if f.From != "" {
		conditions = append(conditions, `e.spent_on >= ?`)
		args = append(args, f.From)
	}
	if f.To != "" {
		conditions = append(conditions, `e.spent_on < ?`)
		args = append(args, f.To)
	}
	if f.ProjectID != 0 {
		conditions = append(conditions, `e.project_id = ?`)
		args = append(args, f.ProjectID)
	}
	if f.CustomerID != 0 {
		conditions = append(conditions, `p.customer_id = ?`)
		args = append(args, f.CustomerID)
	}
	if len(f.Statuses) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",")
		conditions = append(conditions, `e.status IN (`+placeholders+`)`)
		for _, s := range f.Statuses {
			args = append(args, string(s))
		}
	}
	if scoped, scopeArgs := f.Scope.condition("e.project_id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}

	query := expenseSelect + whereClause(conditions) + ` ORDER BY e.spent_on DESC, e.id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var expenses []domain.Expense
	for rows.Next() {
		e, err := scanExpense(rows)
		if err != nil {
			return nil, err
		}
		expenses = append(expenses, e)
	}
	return expenses, rows.Err()
}

func scanExpense(row rowScanner) (domain.Expense, error) {
	var e domain.Expense
	var status, createdAt, updatedAt, unit string
	var billable, reimbursable int

	err := row.Scan(&e.ID, &e.UserID, &e.EnteredBy, &e.ProjectID, &e.SpentOn, &e.Category,
		&e.Description, &e.AmountMinor, &e.Currency, &billable, &reimbursable,
		&e.MarkupPercent, &e.BilledMinor, &e.QuantityMilli, &unit, &e.UnitRateMinor,
		&status, &createdAt, &updatedAt,
		&e.ProjectName, &e.CustomerID, &e.ColourKey, &e.CustomerName, &e.UserName, &e.EnteredByName,
		&e.AttachmentCount)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Expense{}, ErrNotFound
	}
	if err != nil {
		return domain.Expense{}, err
	}

	e.Billable = billable != 0
	e.Reimbursable = reimbursable != 0
	e.Unit = domain.ExpenseUnit(unit)
	e.Status = domain.EntryStatus(status)
	if e.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Expense{}, err
	}
	if e.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Expense{}, err
	}
	return e, nil
}
