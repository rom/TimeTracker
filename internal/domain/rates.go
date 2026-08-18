package domain

import (
	"fmt"
	"strconv"
	"strings"
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

// TravelBilling says how a customer pays for travel time.
type TravelBilling string

const (
	// TravelAsWork is the default: travel bills exactly like work.
	TravelAsWork TravelBilling = ""
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
	// ExpenseBillable is the default: an expense is invoiced to the customer.
	ExpenseBillable ExpenseBilling = ""
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
	case TravelAsWork, TravelAtRate, TravelUnbilled:
	default:
		return invalid("unknown travel billing rule %q", r.TravelBilling)
	}
	switch r.ExpenseBilling {
	case ExpenseBillable, ExpenseNotBilled:
	default:
		return invalid("unknown expense billing rule %q", r.ExpenseBilling)
	}
	return nil
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
