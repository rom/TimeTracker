package service

import (
	"context"

	"github.com/rom/timetracker/internal/domain"
)

// This file resolves what an entry is worth: which hourly rate applies, which
// rounding rule applies, and the amount that follows from the two.
//
// Two rules govern everything here, and both come from
// docs/adr/0014-exact-money-and-duration.md:
//
//   - Rounding is applied at exactly one point - when an entry's billable
//     duration is derived, before it is multiplied by a rate. Totals are the sum
//     of already-rounded per-entry amounts, never a rounding of a sum, so a
//     client can reconcile an invoice line by line.
//   - The resolved rate and the rule that was applied are stored ON the entry.
//     A later rate change or policy change therefore cannot silently rewrite a
//     figure that has already been invoiced.

// billingContext is the hierarchy an entry's rate and rounding are resolved
// from.
type billingContext struct {
	assignment domain.Assignment
	project    domain.Project
	customer   domain.Customer
	// memberRateMinor is the acting person's rate on this project, 0 when none
	// is set. It is the level between the assignment and the project, and it is
	// what expresses "a senior costs more than a junior on the same work".
	memberRateMinor int64
	// rules are the contract terms in force for this project on the day the
	// entry belongs to. Resolved at load time rather than carried on the
	// customer, because terms are dated and can also attach to the project
	// (docs/adr/0026-dated-contract-terms.md).
	rules domain.RateRules
	// defaults are the instance-wide fallbacks from the settings row.
	defaultRateMinor int64
	defaultCurrency  string
	defaultRounding  string
}

// billingContextFor loads the hierarchy for an entry on a given day.
//
// The day matters: contract terms are dated, so an entry backdated into an
// earlier period prices at the terms that were in force then. Callers pass the
// entry's own local day rather than today.
func (s *Service) billingContextFor(ctx context.Context, assignmentID, subjectID int64, day string) (billingContext, error) {
	assignment, err := s.db.GetAssignment(ctx, assignmentID)
	if err != nil {
		return billingContext{}, err
	}
	project, err := s.db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		return billingContext{}, err
	}
	customer, err := s.db.GetCustomer(ctx, project.CustomerID)
	if err != nil {
		return billingContext{}, err
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return billingContext{}, err
	}
	// The per-person rate is looked up for whoever the time belongs to, not for
	// whoever is recording it - otherwise a manager entering time on behalf of a
	// junior would bill it at the manager's rate.
	memberRate, err := s.db.MemberRateMinor(ctx, subjectID, project.ID)
	if err != nil {
		return billingContext{}, err
	}
	rules, err := s.db.ResolveTerms(ctx, customer.ID, project.ID, day)
	if err != nil {
		return billingContext{}, err
	}

	return billingContext{
		assignment:       assignment,
		project:          project,
		customer:         customer,
		memberRateMinor:  memberRate,
		rules:            rules,
		defaultRateMinor: settings.DefaultRateMinor,
		defaultCurrency:  settings.DefaultCurrency,
		defaultRounding:  settings.DefaultRounding,
	}, nil
}

// rateForKind resolves the hourly rate for a kind of time, and whether that kind
// is billable to this customer at all.
//
// Two steps, deliberately separate. The base rate comes from the
// assignment/project/customer hierarchy exactly as it always has - overtime and
// travel do not get their own hierarchy, because a customer's contract sets one
// overtime term for the engagement, not one per assignment. The customer's rules
// then modify that base.
//
// The second result is false only for travel a customer does not pay for. The
// time is still recorded in full; it simply carries no amount, which is a
// different statement from "worth zero per hour" and reads differently on a
// timesheet.
func (b billingContext) rateForKind(kind domain.EntryKind) (domain.Money, bool) {
	return b.rules.RateForKind(kind, b.rate())
}

// rate resolves the hourly rate, most specific level first.
//
// The order is assignment → person-on-project → project → customer → instance
// default. A rate of zero at any level means "inherit", not "free": billing
// something at nothing is almost always a missing configuration rather than an
// intention, and treating zero as a real rate would hide that.
func (b billingContext) rate() domain.Money {
	currency := b.currency()
	switch {
	case b.assignment.RateMinor > 0:
		return domain.NewMoney(b.assignment.RateMinor, currency)
	case b.memberRateMinor > 0:
		return domain.NewMoney(b.memberRateMinor, currency)
	case b.project.RateMinor > 0:
		return domain.NewMoney(b.project.RateMinor, currency)
	case b.customer.RateMinor > 0:
		return domain.NewMoney(b.customer.RateMinor, currency)
	default:
		return domain.NewMoney(b.defaultRateMinor, currency)
	}
}

// currency resolves which currency the amount is in. It follows the customer,
// because that is who gets invoiced; an instance default covers a customer set
// up without one.
func (b billingContext) currency() string {
	if b.customer.Currency != "" {
		return b.customer.Currency
	}
	return b.defaultCurrency
}

// rounding resolves the billing increment rule: project, then customer, then the
// instance default.
func (b billingContext) rounding() domain.RoundingRule {
	if b.project.RoundingRule != "" {
		return domain.ParseRoundingRule(b.project.RoundingRule)
	}
	if b.defaultRounding != "" {
		return domain.ParseRoundingRule(b.defaultRounding)
	}
	return domain.NoRounding
}

// applyBilling writes the billing snapshot onto an entry.
//
// It is called whenever an entry's duration becomes known or changes - on stop,
// on manual creation and on edit - and never at report time. Computing amounts
// when a report is run would mean a report re-run next year could produce a
// different number from the one that was invoiced.
//
// A non-billable entry gets a zeroed snapshot rather than a hidden amount, so
// nothing can later start billing it by accident.
func (b billingContext) applyBilling(entry *domain.TimeEntry) {
	rate, kindBillable := b.rateForKind(entry.KindOrDefault())

	// kindBillable is the customer saying they do not pay for this sort of time
	// - travel, in practice. The entry keeps its own Billable flag untouched:
	// that is the person's statement about the work, and overwriting it would
	// silently rewrite their intent if the contract later changed.
	if !entry.Billable || !kindBillable || entry.DurationSeconds <= 0 {
		entry.RoundingRuleApplied = ""
		entry.BillableSeconds = 0
		entry.RateMinor = 0
		entry.AmountMinor = 0
		entry.Currency = ""
		return
	}

	rule := b.rounding()

	entry.RoundingRuleApplied = rule.String()
	entry.BillableSeconds = rule.Apply(entry.DurationSeconds)
	entry.RateMinor = rate.Minor
	entry.Currency = rate.Currency
	// The multiplication rounds half away from zero at the last minor unit; see
	// domain.Money.MulDurationHours.
	entry.AmountMinor = rate.MulDurationHours(entry.BillableSeconds).Minor
}

// applyBillingTo loads the hierarchy and writes the snapshot onto an entry.
//
// The terms are resolved for the entry's own day, so moving an entry to a date
// in a different contract period re-prices it at the terms that applied then.
func (s *Service) applyBillingTo(ctx context.Context, entry *domain.TimeEntry) error {
	context, err := s.billingContextFor(ctx, entry.AssignmentID, entry.UserID, entry.LocalDay().Format("2006-01-02"))
	if err != nil {
		return err
	}
	context.applyBilling(entry)
	return nil
}
