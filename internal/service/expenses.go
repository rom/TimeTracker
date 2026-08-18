package service

import (
	"context"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// ExpenseInput is what a user supplies when recording a cost.
type ExpenseInput struct {
	ProjectID     int64
	SpentOn       string // YYYY-MM-DD
	Category      string
	Description   string
	Amount        string // as typed; parsed with the project's currency
	Billable      bool
	Reimbursable  bool
	MarkupPercent int64
	// MarkupGiven distinguishes "the user typed 0%" from "the user said nothing
	// about markup". Only the second inherits the customer's default; silently
	// overriding an explicit zero would put a margin on a claim somebody had
	// deliberately made at cost.
	MarkupGiven bool
	// A quantity-priced claim: a distance in kilometres or a number of days, as
	// typed. Priced at the customer's mileage rate or per diem.
	Quantity string
	Unit     domain.ExpenseUnit
	// OnBehalfOf, when set to another user, makes this a proposal that requires
	// that user's confirmation - the same rule as for time.
	OnBehalfOf int64
}

// quantityFrom parses a typed quantity into thousandths of a unit.
func quantityFrom(in ExpenseInput) (int64, error) {
	if in.Unit == domain.UnitNone || in.Quantity == "" {
		return 0, nil
	}
	if !in.Unit.Valid() {
		return 0, fmt.Errorf("%w: unknown unit %q", ErrValidation, in.Unit)
	}
	milli, err := domain.ParseQuantityMilli(in.Quantity)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrValidation, err)
	}
	return milli, nil
}

// CreateExpense records a cost.
func (s *Service) CreateExpense(ctx context.Context, in ExpenseInput) (domain.Expense, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.Expense{}, err
	}

	project, err := s.db.GetProject(ctx, in.ProjectID)
	if err != nil {
		return domain.Expense{}, err
	}

	subjectID := actor.ID
	status := domain.StatusConfirmed
	action := auth.ActionCreate
	if in.OnBehalfOf != 0 && in.OnBehalfOf != actor.ID {
		subjectID = in.OnBehalfOf
		status = domain.StatusPending
		action = auth.ActionProxy
	}

	if err := s.authz.Can(ctx, action, auth.Resource{
		Type: "expense", OwnerID: subjectID,
		ProjectID: project.ID, CustomerID: project.CustomerID,
	}); err != nil {
		return domain.Expense{}, err
	}

	currency := project.Currency
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return domain.Expense{}, err
	}
	if currency == "" {
		currency = settings.DefaultCurrency
	}

	amount, err := domain.ParseMoney(in.Amount, currency)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("%w: %s", ErrValidation, err)
	}

	quantity, err := quantityFrom(in)
	if err != nil {
		return domain.Expense{}, err
	}

	expense := domain.Expense{
		UserID:        subjectID,
		EnteredBy:     actor.ID,
		ProjectID:     in.ProjectID,
		SpentOn:       in.SpentOn,
		Category:      in.Category,
		Description:   in.Description,
		AmountMinor:   amount.Minor,
		Currency:      currency,
		Billable:      in.Billable,
		Reimbursable:  in.Reimbursable,
		MarkupPercent: in.MarkupPercent,
		QuantityMilli: quantity,
		Unit:          in.Unit,
		Status:        status,
	}
	// The customer's reimbursement terms fill in what the user did not say: the
	// default markup, the mileage rate or per diem that prices a quantity, and
	// whether this customer is invoiced for expenses at all.
	// The terms in force on the day the cost was incurred, not today's: a claim
	// entered late still belongs to the contract period it was spent in.
	rules, err := s.db.ResolveTerms(ctx, project.CustomerID, project.ID, in.SpentOn)
	if err != nil {
		return domain.Expense{}, err
	}
	applyCustomerExpenseRules(&expense, rules, in.MarkupGiven)

	// The billed amount is computed once and frozen, for the same reason a time
	// entry freezes its rate: a markup change tomorrow must not alter what was
	// invoiced today.
	expense.ApplyMarkup()

	if err := expense.Validate(); err != nil {
		return domain.Expense{}, err
	}

	created, err := s.db.CreateExpense(ctx, expense)
	if err != nil {
		return domain.Expense{}, err
	}
	if err := s.recordAudit(ctx, "expense.create", "expense", created.ID, map[string]any{
		"project":      project.Name,
		"amount_minor": created.AmountMinor,
		"currency":     created.Currency,
		"billable":     created.Billable,
		"reimbursable": created.Reimbursable,
		"status":       string(created.Status),
	}); err != nil {
		return domain.Expense{}, err
	}
	return s.db.GetExpense(ctx, created.ID)
}

// UpdateExpense saves an edited expense.
func (s *Service) UpdateExpense(ctx context.Context, id int64, in ExpenseInput) (domain.Expense, error) {
	existing, err := s.db.GetExpense(ctx, id)
	if err != nil {
		return domain.Expense{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionUpdate, auth.Resource{
		Type: "expense", ID: id, OwnerID: existing.UserID,
		ProjectID: existing.ProjectID, CustomerID: existing.CustomerID,
	}); err != nil {
		return domain.Expense{}, notFoundFor(err)
	}

	project, err := s.db.GetProject(ctx, in.ProjectID)
	if err != nil {
		return domain.Expense{}, err
	}
	currency := project.Currency
	if currency == "" {
		currency = existing.Currency
	}
	amount, err := domain.ParseMoney(in.Amount, currency)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("%w: %s", ErrValidation, err)
	}

	updated := existing
	updated.ProjectID = in.ProjectID
	updated.SpentOn = in.SpentOn
	updated.Category = in.Category
	updated.Description = in.Description
	updated.AmountMinor = amount.Minor
	updated.Currency = currency
	updated.Billable = in.Billable
	updated.Reimbursable = in.Reimbursable
	updated.MarkupPercent = in.MarkupPercent

	quantity, err := quantityFrom(in)
	if err != nil {
		return domain.Expense{}, err
	}
	updated.QuantityMilli = quantity
	updated.Unit = in.Unit
	// The unit rate is re-resolved rather than carried over, so correcting a
	// distance re-prices it at the rate that applies now.
	updated.UnitRateMinor = 0

	rules, err := s.db.ResolveTerms(ctx, project.CustomerID, project.ID, in.SpentOn)
	if err != nil {
		return domain.Expense{}, err
	}
	applyCustomerExpenseRules(&updated, rules, in.MarkupGiven)
	updated.ApplyMarkup()

	if err := updated.Validate(); err != nil {
		return domain.Expense{}, err
	}
	if err := s.db.UpdateExpense(ctx, updated); err != nil {
		return domain.Expense{}, err
	}
	if err := s.recordAudit(ctx, "expense.update", "expense", id, map[string]any{
		"amount_minor": map[string]any{"from": existing.AmountMinor, "to": updated.AmountMinor},
		"billable":     map[string]any{"from": existing.Billable, "to": updated.Billable},
		"reimbursable": map[string]any{"from": existing.Reimbursable, "to": updated.Reimbursable},
	}); err != nil {
		return domain.Expense{}, err
	}
	return s.db.GetExpense(ctx, id)
}

// DeleteExpense removes an expense, and any attachments it owned.
func (s *Service) DeleteExpense(ctx context.Context, id int64) error {
	existing, err := s.db.GetExpense(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionDelete, auth.Resource{
		Type: "expense", ID: id, OwnerID: existing.UserID,
		ProjectID: existing.ProjectID, CustomerID: existing.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}

	// The receipts go with it. Removing the references here leaves the blobs
	// unreferenced, and the sweep collects them.
	if err := s.db.DeleteExpense(ctx, id); err != nil {
		return err
	}
	return s.recordAudit(ctx, "expense.delete", "expense", id, map[string]any{
		"amount_minor": existing.AmountMinor,
		"currency":     existing.Currency,
		"description":  existing.Description,
	})
}

// Expense loads one expense, authorised.
func (s *Service) Expense(ctx context.Context, id int64) (domain.Expense, error) {
	expense, err := s.db.GetExpense(ctx, id)
	if err != nil {
		return domain.Expense{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "expense", ID: id, OwnerID: expense.UserID,
		ProjectID: expense.ProjectID, CustomerID: expense.CustomerID,
	}); err != nil {
		return domain.Expense{}, notFoundFor(err)
	}
	return expense, nil
}

// ExpenseFilter narrows a query over expenses.
type ExpenseFilter struct {
	From       time.Time
	To         time.Time
	ProjectID  int64
	CustomerID int64
	UserID     int64
	Limit      int
}

// Expenses lists the expenses the actor may see.
func (s *Service) Expenses(ctx context.Context, filter ExpenseFilter) ([]domain.Expense, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, listResource(ctx, "expense")); err != nil {
		return nil, err
	}

	subjectID := actor.ID
	if filter.UserID != 0 && filter.UserID != actor.ID {
		if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
			Type: "expense", OwnerID: filter.UserID, ProjectID: filter.ProjectID,
			CustomerID: filter.CustomerID,
		}); err != nil {
			return nil, notFoundFor(err)
		}
		subjectID = filter.UserID
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}

	storeFilter := store.ExpenseFilter{
		UserID:     subjectID,
		ProjectID:  filter.ProjectID,
		CustomerID: filter.CustomerID,
		Scope:      scope,
		Limit:      filter.Limit,
	}
	if !filter.From.IsZero() {
		storeFilter.From = filter.From.Format("2006-01-02")
	}
	if !filter.To.IsZero() {
		storeFilter.To = filter.To.Format("2006-01-02")
	}
	return s.db.ListExpenses(ctx, storeFilter)
}

// ExpenseTotals summarises a set of expenses.
//
// Billable and reimbursable are totalled separately, and per currency, because
// they are different kinds of money and there is no conversion anywhere in this
// application.
type ExpenseTotals struct {
	// BillableByCurrency is what joins an invoice, including markup.
	BillableByCurrency map[string]int64
	// ReimbursableByCurrency is what is owed back to a person, at cost.
	ReimbursableByCurrency map[string]int64
	Count                  int
}

// Totals computes expense totals.
func (s *Service) ExpenseTotals(expenses []domain.Expense) ExpenseTotals {
	totals := ExpenseTotals{
		BillableByCurrency:     map[string]int64{},
		ReimbursableByCurrency: map[string]int64{},
	}
	for _, e := range expenses {
		if !e.Counts() {
			continue
		}
		totals.Count++
		if e.Billable {
			totals.BillableByCurrency[e.Currency] += e.BilledMinor
		}
		if e.Reimbursable {
			// Reimbursement is at cost: a markup is what the client pays, not
			// what the person gets back.
			totals.ReimbursableByCurrency[e.Currency] += e.AmountMinor
		}
	}
	return totals
}
