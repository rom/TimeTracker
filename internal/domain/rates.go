package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Customer-specific rate rules: overtime, travel time, and reimbursement.
//
// The base hourly rate says what an hour is worth. These say what an hour is
// worth when it is the tenth one that day, what it is worth when it is spent on
// a train, and what gets paid back and against what evidence. They are contract
// terms, which is why they live on the customer: every customer's contract is
// different, and a global setting would be wrong for all of them.
//
// Every field is optional. A customer with none of them set bills exactly as it
// did before this existed.

// EntryKind is what sort of time an entry records.
//
// The kind is chosen by the person, not derived from a threshold. Whether a
// particular hour counts as overtime is a contractual judgement - it may need
// authorisation, it may not apply to salaried staff, it may have been agreed
// in advance for that week only. A tool that silently bills the ninth hour at
// time and a half because somebody forgot to stop a timer is manufacturing an
// invoice dispute. The thresholds prompt; the person decides.
type EntryKind string

const (
	// KindWork is ordinary work, and the default. Every entry recorded before
	// this feature existed is work, so no history changes meaning.
	KindWork EntryKind = "work"
	// KindOvertime is time worked beyond the agreed hours.
	KindOvertime EntryKind = "overtime"
	// KindTravel is time spent travelling for the customer. Frequently billed
	// at a reduced rate, and just as frequently not billed at all.
	KindTravel EntryKind = "travel"
)

// EntryKinds returns the kinds in the order they should be offered.
func EntryKinds() []EntryKind { return []EntryKind{KindWork, KindOvertime, KindTravel} }

// Valid reports whether the kind is one this application knows.
func (k EntryKind) Valid() bool {
	switch k {
	case KindWork, KindOvertime, KindTravel:
		return true
	}
	return false
}

// MessageKey is the catalogue key naming this kind.
func (k EntryKind) MessageKey() string { return "kind." + string(k) }

// TravelBilling says how travel time is paid for.
type TravelBilling string

const (
	// TravelInherit is unset: take whatever the wider scope says, and bill
	// travel as work if nothing does.
	//
	// Distinct from TravelAsWork because terms now nest - a project's terms are
	// merged over its customer's - and "say nothing about travel" has to mean
	// something different from "travel is billed as work here". Without the
	// distinction, a project setting only its overtime would silently cancel
	// its customer's travel terms.
	TravelInherit TravelBilling = ""
	// TravelAsWork bills travel exactly like work, said explicitly. It is how a
	// project overrides a customer that does not pay for travel.
	TravelAsWork TravelBilling = "work"
	// TravelAtRate bills travel at its own rate or multiplier.
	TravelAtRate TravelBilling = "rate"
	// TravelUnbilled records travel in full but never invoices it. A distinct
	// state rather than a zero rate: the timesheet still has to show the time,
	// and "we do not bill travel" is a different statement from "travel is
	// worth nothing per hour".
	TravelUnbilled TravelBilling = "unbilled"
)

// ExpenseBilling says whether expenses reach the customer's invoice.
type ExpenseBilling string

const (
	// ExpenseInherit is unset, on the same reasoning as TravelInherit.
	ExpenseInherit ExpenseBilling = ""
	// ExpenseBillable invoices expenses to the customer, said explicitly.
	ExpenseBillable ExpenseBilling = "yes"
	// ExpenseNotBilled means expenses are reimbursed to the person who paid but
	// never invoiced - a fixed-price engagement, typically.
	ExpenseNotBilled ExpenseBilling = "no"
)

// RateRules are one customer's contractual rules beyond the base hourly rate.
//
// Money fields are minor units and percentages are whole percent, both integers,
// for the reason every other amount in this application is
// (docs/adr/0014-exact-money-and-duration.md). Zero means "not set" throughout,
// except where a separate string field exists to express a deliberate zero.
type RateRules struct {
	// OvertimeRateMinor is an absolute hourly rate for overtime. It wins over
	// the multiplier: a contract naming a figure is naming it instead of a
	// multiple, not as well as one.
	OvertimeRateMinor int64
	// OvertimeMultiplierPct is overtime as a percentage of the resolved base
	// rate. 150 is time and a half, 200 is double time.
	OvertimeMultiplierPct int64
	// The thresholds drive a prompt and nothing else. Seconds; 0 disables.
	OvertimeDailyThresholdSeconds  int64
	OvertimeWeeklyThresholdSeconds int64

	TravelBilling       TravelBilling
	TravelRateMinor     int64
	TravelMultiplierPct int64

	// ExpenseMarkupPct is the default markup on the billable side of an expense.
	ExpenseMarkupPct int64
	ExpenseBilling   ExpenseBilling
	// MileageRateMinor is paid per kilometre, PerDiemMinor per day. 0 means the
	// customer has no such rule and the amount is entered by hand.
	MileageRateMinor int64
	PerDiemMinor     int64
	// ReceiptRequiredAboveMinor refuses an expense above this amount unless a
	// receipt is attached. 0 disables the requirement.
	ReceiptRequiredAboveMinor int64
}

// Configured reports whether any rule is set, so the interface can stay quiet
// about a customer that has none.
func (r RateRules) Configured() bool {
	return r != RateRules{}
}

// Merge lays these rules over a wider scope's, field by field.
//
// A field set here wins; a field left unset takes the base's value. Field-level
// rather than whole-record, so a project that differs only in overtime says
// only that and the rest keeps following the account. Restating a customer's
// whole contract on each of its projects would be the alternative, and those
// copies would drift the moment one of them was renegotiated.
//
// "Unset" is zero for the numbers and the empty string for the two enumerations,
// which is why those needed an explicit value for their default: a project has
// to be able to say "travel is billed as work here" as something other than
// silence.
func (r RateRules) Merge(base RateRules) RateRules {
	merged := base

	overlay := func(dst *int64, value int64) {
		if value != 0 {
			*dst = value
		}
	}
	overlay(&merged.OvertimeRateMinor, r.OvertimeRateMinor)
	overlay(&merged.OvertimeMultiplierPct, r.OvertimeMultiplierPct)
	overlay(&merged.OvertimeDailyThresholdSeconds, r.OvertimeDailyThresholdSeconds)
	overlay(&merged.OvertimeWeeklyThresholdSeconds, r.OvertimeWeeklyThresholdSeconds)
	overlay(&merged.TravelRateMinor, r.TravelRateMinor)
	overlay(&merged.TravelMultiplierPct, r.TravelMultiplierPct)
	overlay(&merged.ExpenseMarkupPct, r.ExpenseMarkupPct)
	overlay(&merged.MileageRateMinor, r.MileageRateMinor)
	overlay(&merged.PerDiemMinor, r.PerDiemMinor)
	overlay(&merged.ReceiptRequiredAboveMinor, r.ReceiptRequiredAboveMinor)

	if r.TravelBilling != TravelInherit {
		merged.TravelBilling = r.TravelBilling
	}
	if r.ExpenseBilling != ExpenseInherit {
		merged.ExpenseBilling = r.ExpenseBilling
	}
	return merged
}

// RateForKind returns the hourly rate for a kind of time, given the base rate
// resolved from the assignment/project/customer hierarchy.
//
// The second result reports whether the time is billable at all, which is how
// "we do not pay for travel" is expressed: the time is still recorded in full,
// it simply carries no amount.
func (r RateRules) RateForKind(kind EntryKind, base Money) (Money, bool) {
	switch kind {
	case KindOvertime:
		if r.OvertimeRateMinor > 0 {
			return NewMoney(r.OvertimeRateMinor, base.Currency), true
		}
		if r.OvertimeMultiplierPct > 0 {
			return base.ScalePercent(r.OvertimeMultiplierPct), true
		}
		// No overtime terms agreed: overtime is worth what work is worth. That
		// is the honest default - inventing a multiplier nobody agreed to would
		// put a number on an invoice that the contract does not support.
		return base, true

	case KindTravel:
		switch r.TravelBilling {
		case TravelUnbilled:
			return NewMoney(0, base.Currency), false
		case TravelAtRate:
			if r.TravelRateMinor > 0 {
				return NewMoney(r.TravelRateMinor, base.Currency), true
			}
			if r.TravelMultiplierPct > 0 {
				return base.ScalePercent(r.TravelMultiplierPct), true
			}
		}
		return base, true

	default:
		return base, true
	}
}

// Validate checks the rules that hold regardless of storage.
func (r RateRules) Validate() error {
	if r.OvertimeRateMinor < 0 || r.TravelRateMinor < 0 ||
		r.MileageRateMinor < 0 || r.PerDiemMinor < 0 ||
		r.ReceiptRequiredAboveMinor < 0 {
		return invalid("a rate cannot be negative")
	}
	// An upper bound catches a decimal point in the wrong place - 15000% rather
	// than 150% - which would otherwise reach an invoice.
	for _, pct := range []int64{r.OvertimeMultiplierPct, r.TravelMultiplierPct, r.ExpenseMarkupPct} {
		if pct < 0 || pct > 1000 {
			return invalid("a percentage must be between 0 and 1000, got %d", pct)
		}
	}
	if r.OvertimeDailyThresholdSeconds < 0 || r.OvertimeWeeklyThresholdSeconds < 0 {
		return invalid("an overtime threshold cannot be negative")
	}
	if r.OvertimeDailyThresholdSeconds > 24*3600 {
		return invalid("a daily overtime threshold cannot exceed 24 hours")
	}
	if r.OvertimeWeeklyThresholdSeconds > 7*24*3600 {
		return invalid("a weekly overtime threshold cannot exceed 168 hours")
	}
	switch r.TravelBilling {
	case TravelInherit, TravelAsWork, TravelAtRate, TravelUnbilled:
	default:
		return invalid("unknown travel billing rule %q", r.TravelBilling)
	}
	switch r.ExpenseBilling {
	case ExpenseInherit, ExpenseBillable, ExpenseNotBilled:
	default:
		return invalid("unknown expense billing rule %q", r.ExpenseBilling)
	}
	return nil
}

// ---------------------------------------------------------- dated terms ----

// TermsScope says what a set of contract terms attaches to.
type TermsScope string

const (
	// TermsForCustomer are the account's terms, the usual case.
	TermsForCustomer TermsScope = "customer"
	// TermsForProject override the account's for one engagement, field by
	// field. A project that differs only in overtime says only that.
	TermsForProject TermsScope = "project"
)

// Valid reports whether the scope is one this application knows.
func (s TermsScope) Valid() bool {
	return s == TermsForCustomer || s == TermsForProject
}

// ContractTerms is one dated set of rules for one customer or project.
//
// Terms are dated because contracts are renegotiated, and because a rate that
// went up in April has to keep answering for March. Entries already freeze the
// amount they were billed at (ADR-0014); dating the terms is what makes a
// *newly recorded* entry, backdated into an earlier period, price at the terms
// that were in force then rather than at today's.
type ContractTerms struct {
	ID      int64
	Scope   TermsScope
	ScopeID int64
	// EffectiveFrom is the first day these terms apply, YYYY-MM-DD. The empty
	// string means "since forever" - which is what terms written before dating
	// existed carry, because there was nothing else for them to mean.
	EffectiveFrom string
	Rules         RateRules
	// Note is why the terms changed. A rate that went up on renewal is
	// explicable years later only if somebody wrote down why.
	Note      string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Denormalised for display.
	ScopeName string
}

// AppliesOn reports whether these terms are in force on a date.
func (t ContractTerms) AppliesOn(day string) bool {
	return t.EffectiveFrom == "" || t.EffectiveFrom <= day
}

// Validate checks the rules that hold regardless of storage.
func (t ContractTerms) Validate() error {
	if !t.Scope.Valid() {
		return invalid("unknown terms scope %q", t.Scope)
	}
	if t.ScopeID == 0 {
		return invalid("contract terms must belong to a customer or a project")
	}
	if t.EffectiveFrom != "" {
		if _, err := time.Parse("2006-01-02", t.EffectiveFrom); err != nil {
			return invalid("%q is not a date", t.EffectiveFrom)
		}
	}
	if len(t.Note) > 1000 {
		return invalid("the note is too long (max 1000 characters)")
	}
	return t.Rules.Validate()
}

// ResolveTerms merges a customer's and a project's terms for one day.
//
// The latest customer terms in force on that day are the base; the latest
// project terms in force on that day are laid over them field by field. Both
// lists must be ordered newest first, which is how the store returns them.
//
// One function so that the resolution order exists in exactly one place: the
// billing path, the expense path and the screen that previews the terms all ask
// the same question and must get the same answer.
func ResolveTerms(customerTerms, projectTerms []ContractTerms, day string) RateRules {
	rules := latestApplicable(customerTerms, day)
	return latestApplicable(projectTerms, day).Merge(rules)
}

// latestApplicable returns the first set of terms in force on a day, from a list
// ordered newest first.
func latestApplicable(terms []ContractTerms, day string) RateRules {
	for _, candidate := range terms {
		if candidate.AppliesOn(day) {
			return candidate.Rules
		}
	}
	return RateRules{}
}

// ExpenseUnit is how a quantity-priced expense is measured.
type ExpenseUnit string

const (
	// UnitNone is an ordinary expense with an amount typed straight in.
	UnitNone ExpenseUnit = ""
	// UnitKilometre prices a distance at the customer's mileage rate.
	UnitKilometre ExpenseUnit = "km"
	// UnitDay prices a number of days at the customer's per diem.
	UnitDay ExpenseUnit = "day"
)

// Valid reports whether the unit is one this application knows.
func (u ExpenseUnit) Valid() bool {
	switch u {
	case UnitNone, UnitKilometre, UnitDay:
		return true
	}
	return false
}

// UnitRateFor returns the customer's rate for a unit, and whether one is set.
func (r RateRules) UnitRateFor(unit ExpenseUnit) (int64, bool) {
	switch unit {
	case UnitKilometre:
		return r.MileageRateMinor, r.MileageRateMinor > 0
	case UnitDay:
		return r.PerDiemMinor, r.PerDiemMinor > 0
	}
	return 0, false
}

// QuantityAmount prices a quantity at a unit rate.
//
// The quantity is in thousandths of a unit, so 42.5 km is 42500 - exact, and no
// float ever touches a stored field. The multiplication rounds half away from
// zero at the last minor unit, matching how an hourly amount is computed.
func QuantityAmount(quantityMilli, unitRateMinor int64, currency string) Money {
	if quantityMilli <= 0 || unitRateMinor <= 0 {
		return NewMoney(0, currency)
	}
	const milli = 1000
	product := quantityMilli * unitRateMinor
	minor := product / milli
	if remainder := product % milli; remainder*2 >= milli {
		minor++
	}
	return NewMoney(minor, currency)
}

// ParseQuantityMilli reads a typed quantity into thousandths of a unit.
//
// Accepts a decimal comma as well as a point, because a Swedish keyboard
// produces "42,5" and refusing it would be a needless obstacle - the same
// accommodation ParseMoney makes.
func ParseQuantityMilli(text string) (int64, error) {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return 0, nil
	}
	cleaned = strings.ReplaceAll(cleaned, ",", ".")
	cleaned = strings.ReplaceAll(cleaned, " ", "")

	negative := false
	if strings.HasPrefix(cleaned, "-") {
		negative, cleaned = true, cleaned[1:]
	}

	whole, frac, _ := strings.Cut(cleaned, ".")
	if whole == "" {
		whole = "0"
	}
	// Pad or truncate to exactly three decimals. Truncating rather than
	// rounding: a quantity typed to four decimals is a typo, and silently
	// rounding it would hide that.
	for len(frac) < 3 {
		frac += "0"
	}
	if len(frac) > 3 {
		frac = frac[:3]
	}

	wholeValue, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, invalid("%q is not a quantity", text)
	}
	fracValue, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, invalid("%q is not a quantity", text)
	}
	if wholeValue > 1_000_000 {
		return 0, invalid("quantity %q is implausibly large", text)
	}

	milli := wholeValue*1000 + fracValue
	if negative {
		return 0, invalid("a quantity cannot be negative")
	}
	return milli, nil
}

// FormatQuantity renders a thousandths quantity as a plain decimal, trimmed.
//
// Used in exports and form fields, so what is displayed can be typed back.
func FormatQuantity(quantityMilli int64) string {
	if quantityMilli == 0 {
		return ""
	}
	whole := quantityMilli / 1000
	frac := quantityMilli % 1000
	if frac < 0 {
		frac = -frac
	}
	if frac == 0 {
		return fmt.Sprintf("%d", whole)
	}
	text := fmt.Sprintf("%d.%03d", whole, frac)
	// Trim trailing zeros so 42.500 reads as 42.5.
	for len(text) > 0 && text[len(text)-1] == '0' {
		text = text[:len(text)-1]
	}
	return text
}
