package service

import (
	"context"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Applying a customer's contract rules: overtime, travel time, reimbursement.
//
// The rules themselves live on the customer (domain/rates.go). This file is
// where they meet a real entry or expense - which is deliberately a small
// amount of code, because the rules were designed to be data rather than
// branches.

// ---------------------------------------------------------------- overtime --

// OvertimeNotice reports a day or a week that has passed the customer's
// threshold without any of the time being marked as overtime.
//
// A notice, never an automatic reclassification. Whether a particular hour is
// billable as overtime is a contractual judgement - it may need authorisation,
// it may not apply to salaried staff, it may have been agreed for that week
// only. Silently billing hour nine at time and a half because somebody forgot
// to stop a timer manufactures an invoice dispute. So the tool reports what it
// observed and leaves the decision where it belongs, exactly as it does with
// unaccounted-time gaps.
type OvertimeNotice struct {
	CustomerID   int64
	CustomerName string
	// Period is "day" or "week", and Starting is the day it began.
	Period   string
	Starting time.Time
	// ThresholdSeconds is what the customer's contract allows before overtime
	// terms apply; RecordedSeconds is what is actually recorded.
	ThresholdSeconds int64
	RecordedSeconds  int64
	// OvertimeSeconds is how much is already marked as overtime, so a day that
	// has been dealt with stops being reported.
	OvertimeSeconds int64
}

// ExcessSeconds is how far past the threshold the unmarked time reaches.
func (n OvertimeNotice) ExcessSeconds() int64 {
	excess := n.RecordedSeconds - n.ThresholdSeconds
	if excess < 0 {
		return 0
	}
	return excess
}

// OvertimeNotices reports where the acting user's recorded time has passed a
// customer's overtime threshold without being marked as overtime.
//
// Scoped to one week, because that is the span a person actually reviews before
// submitting, and a notice about a day three months ago is noise.
func (s *Service) OvertimeNotices(ctx context.Context, date time.Time) ([]OvertimeNotice, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return nil, err
	}

	loc := locationFor(actor)
	weekStart := domain.WeekStartFor(date, settings.WeekStart, loc)
	start, err := domain.ParseWeekStart(weekStart, loc)
	if err != nil {
		return nil, err
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		UserID: actor.ID,
		From:   start,
		To:     domain.WeekEnd(start),
		Scope:  scope,
	})
	if err != nil {
		return nil, err
	}

	// Totals per customer per day, and per customer for the week. Only counted
	// entries: a pending proposal or a flagged entry is not yet somebody's time.
	type bucket struct {
		name      string
		perDay    map[string]int64
		perDayOT  map[string]int64
		weekTotal int64
		weekOT    int64
	}
	buckets := map[int64]*bucket{}
	for _, entry := range entries {
		if !entry.Counts() {
			continue
		}
		b := buckets[entry.CustomerID]
		if b == nil {
			b = &bucket{
				name:     entry.CustomerName,
				perDay:   map[string]int64{},
				perDayOT: map[string]int64{},
			}
			buckets[entry.CustomerID] = b
		}
		day := entry.StartedAt.In(loc).Format("2006-01-02")
		b.perDay[day] += entry.DurationSeconds
		b.weekTotal += entry.DurationSeconds
		if entry.KindOrDefault() == domain.KindOvertime {
			b.perDayOT[day] += entry.DurationSeconds
			b.weekOT += entry.DurationSeconds
		}
	}

	var notices []OvertimeNotice
	for customerID, b := range buckets {
		customer, err := s.db.GetCustomer(ctx, customerID)
		if err != nil {
			return nil, err
		}
		rules := customer.Rules

		if limit := rules.OvertimeDailyThresholdSeconds; limit > 0 {
			for day, seconds := range b.perDay {
				// Already-marked overtime is subtracted: a day that has been
				// dealt with must stop nagging, or the notice trains people to
				// ignore it.
				if seconds-b.perDayOT[day] <= limit {
					continue
				}
				when, parseErr := time.ParseInLocation("2006-01-02", day, loc)
				if parseErr != nil {
					return nil, parseErr
				}
				notices = append(notices, OvertimeNotice{
					CustomerID: customerID, CustomerName: b.name,
					Period: "day", Starting: when,
					ThresholdSeconds: limit,
					RecordedSeconds:  seconds,
					OvertimeSeconds:  b.perDayOT[day],
				})
			}
		}

		if limit := rules.OvertimeWeeklyThresholdSeconds; limit > 0 &&
			b.weekTotal-b.weekOT > limit {
			notices = append(notices, OvertimeNotice{
				CustomerID: customerID, CustomerName: b.name,
				Period: "week", Starting: start,
				ThresholdSeconds: limit,
				RecordedSeconds:  b.weekTotal,
				OvertimeSeconds:  b.weekOT,
			})
		}
	}

	// A stable order, so the same week does not reshuffle its notices between
	// page loads - map iteration alone would.
	sortNotices(notices)
	return notices, nil
}

// sortNotices orders by date, then customer, then period.
func sortNotices(notices []OvertimeNotice) {
	for i := 1; i < len(notices); i++ {
		for j := i; j > 0 && noticeLess(notices[j], notices[j-1]); j-- {
			notices[j], notices[j-1] = notices[j-1], notices[j]
		}
	}
}

func noticeLess(a, b OvertimeNotice) bool {
	if !a.Starting.Equal(b.Starting) {
		return a.Starting.Before(b.Starting)
	}
	if a.CustomerName != b.CustomerName {
		return a.CustomerName < b.CustomerName
	}
	return a.Period < b.Period
}

// ----------------------------------------------------------- reimbursement --

// applyCustomerExpenseRules fills in a customer's reimbursement terms.
//
// It supplies defaults rather than overriding what the user typed: an explicit
// markup stays, an explicit amount stays. The rules exist so that the common
// case needs no thought, not so that the customer's configuration can silently
// rewrite a claim somebody made deliberately.
func applyCustomerExpenseRules(expense *domain.Expense, rules domain.RateRules, markupGiven bool) {
	// A customer who is never invoiced for expenses. The claim is still
	// reimbursed to whoever paid; it simply does not reach an invoice.
	if rules.ExpenseBilling == domain.ExpenseNotBilled {
		expense.Billable = false
	}
	if !markupGiven && expense.MarkupPercent == 0 {
		expense.MarkupPercent = rules.ExpenseMarkupPct
	}

	// A quantity-priced claim: distance or days at the customer's rate. The
	// amount is computed rather than typed, which is the whole point - nobody
	// should be multiplying 42.5 by 2.50 in their head and getting it wrong.
	if expense.Unit != domain.UnitNone && expense.QuantityMilli > 0 {
		rate := expense.UnitRateMinor
		if rate == 0 {
			if customerRate, ok := rules.UnitRateFor(expense.Unit); ok {
				rate = customerRate
			}
		}
		if rate > 0 {
			expense.UnitRateMinor = rate
			expense.AmountMinor = domain.QuantityAmount(
				expense.QuantityMilli, rate, expense.Currency).Minor
		}
	}
}

// NeedsReceipt reports whether an expense is above the customer's evidence
// threshold with nothing attached.
//
// Checked at read time rather than stored, because attaching a receipt must
// clear it immediately and a stored flag would need every attachment path to
// remember to recompute it.
func (s *Service) NeedsReceipt(ctx context.Context, expense domain.Expense) (bool, error) {
	if expense.AttachmentCount > 0 {
		return false, nil
	}
	customer, err := s.db.GetCustomer(ctx, expense.CustomerID)
	if err != nil {
		return false, err
	}
	limit := customer.Rules.ReceiptRequiredAboveMinor
	return limit > 0 && expense.AmountMinor > limit, nil
}

// checkReceiptsPresent refuses a submission while an expense in the week is
// missing evidence its customer's contract requires.
//
// This is the point where the requirement can be enforced. It cannot be
// enforced when the expense is created - an attachment needs an expense to
// belong to - and enforcing it at invoicing time would mean discovering the
// problem when it is most expensive to fix.
func (s *Service) checkReceiptsPresent(ctx context.Context, userID int64, weekStart string, loc *time.Location) error {
	start, err := domain.ParseWeekStart(weekStart, loc)
	if err != nil {
		return err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return err
	}
	expenses, err := s.db.ListExpenses(ctx, store.ExpenseFilter{
		UserID: userID,
		From:   start.Format("2006-01-02"),
		To:     domain.WeekEnd(start).Format("2006-01-02"),
		Scope:  scope,
	})
	if err != nil {
		return err
	}

	for _, expense := range expenses {
		if !expense.Counts() {
			continue
		}
		needs, err := s.NeedsReceipt(ctx, expense)
		if err != nil {
			return err
		}
		if needs {
			return fmt.Errorf(
				"%w: %s on %s needs a receipt before this week can be submitted",
				ErrValidation, expense.Description, expense.SpentOn)
		}
	}
	return nil
}
