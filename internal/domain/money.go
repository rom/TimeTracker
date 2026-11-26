package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrCurrencyMismatch is returned when two Money values in different currencies
// are combined. We refuse rather than guess: there is no exchange rate in this
// application, and silently adding SEK to EUR would produce a number that looks
// plausible on an invoice and is wrong.
var ErrCurrencyMismatch = errors.New("cannot combine amounts in different currencies")

// Money is an exact monetary amount, held as a whole number of minor units
// (cents, öre, pence) together with an ISO-4217 currency code.
//
// It is never a float. Floating point cannot represent 0.1 exactly, so a few
// thousand additions of a rate accumulate an error that surfaces as a one-cent
// discrepancy on a client's invoice - the classic defect this type exists to make
// impossible. See docs/adr/0014-exact-money-and-duration.md.
//
// The zero value is a valid zero amount with no currency, which combines with
// anything; this makes summing a list starting from a zero accumulator work
// without special cases.
type Money struct {
	// Minor is the amount in minor units. 1234 with Currency "EUR" is 12.34 EUR.
	Minor int64
	// Currency is an ISO-4217 code such as "EUR", "SEK" or "USD". Empty means
	// "unset", which only a zero amount may have.
	Currency string
}

// CurrencyExponent is how many decimal places a currency's minor unit is worth,
// as ISO 4217 defines it.
//
// Two for most of the world, and this application held every currency at two
// for a long time - which means an instance invoicing in yen held every amount
// at a hundred times its value, because a yen has no subdivision to hold. The
// currency code is validated as any three letters, so nothing stopped it.
//
// The exceptions are a short, stable list rather than a lookup: it has changed
// perhaps a dozen times in forty years, and a table in the source can be read
// and disagreed with. An unlisted code gets two, which is right for every
// currency not named here and is the safe way to be wrong about a new one - the
// amount is then out by a factor people notice immediately on the first
// invoice, rather than by one they do not.
func CurrencyExponent(currency string) int {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	// No minor unit at all. An amount is a whole number of them.
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG",
		"RWF", "UGX", "UYI", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	// A thousandth, written with three decimal places. The Gulf dinars and the
	// Tunisian and Libyan dinar.
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	// Four, for the two units of account that use them.
	case "CLF", "UYW":
		return 4
	default:
		return 2
	}
}

// minorUnitsPer is 10^CurrencyExponent: how many minor units make one whole
// unit of the currency.
func minorUnitsPer(currency string) int64 {
	scale := int64(1)
	for i := 0; i < CurrencyExponent(currency); i++ {
		scale *= 10
	}
	return scale
}

// NewMoney builds an amount from a whole number of minor units.
func NewMoney(minor int64, currency string) Money {
	return Money{Minor: minor, Currency: strings.ToUpper(strings.TrimSpace(currency))}
}

// IsZero reports whether the amount is zero, regardless of currency.
func (m Money) IsZero() bool { return m.Minor == 0 }

// Add returns m+other, or an error if the currencies differ.
//
// A zero amount with no currency adopts the other currency, so that
// `total, _ = total.Add(x)` works when total starts from the zero value.
func (m Money) Add(other Money) (Money, error) {
	cur, err := combineCurrency(m, other)
	if err != nil {
		return Money{}, err
	}
	return Money{Minor: m.Minor + other.Minor, Currency: cur}, nil
}

// Sub returns m-other, or an error if the currencies differ.
func (m Money) Sub(other Money) (Money, error) {
	cur, err := combineCurrency(m, other)
	if err != nil {
		return Money{}, err
	}
	return Money{Minor: m.Minor - other.Minor, Currency: cur}, nil
}

// combineCurrency decides the currency of a two-operand result, tolerating the
// unset currency of a zero value on either side.
func combineCurrency(a, b Money) (string, error) {
	switch {
	case a.Currency == b.Currency:
		return a.Currency, nil
	case a.Currency == "" && a.Minor == 0:
		return b.Currency, nil
	case b.Currency == "" && b.Minor == 0:
		return a.Currency, nil
	default:
		return "", fmt.Errorf("%w: %q and %q", ErrCurrencyMismatch, a.Currency, b.Currency)
	}
}

// MulDurationHours multiplies an hourly rate by a duration and returns the amount
// to bill.
//
// The arithmetic is done entirely in integers: minor units per hour multiplied by
// seconds, divided by 3600, rounding half away from zero at the final minor unit.
// Half-away-from-zero is the convention people expect on an invoice (0.5 rounds to
// 1, -0.5 to -1); banker's rounding would surprise a client reconciling a line.
//
// The intermediate product can be large - a 500,00 currency-unit hourly rate over
// a year of seconds is well within int64, so no overflow check is needed for any
// realistic input.
func (m Money) MulDurationHours(seconds int64) Money {
	const secondsPerHour = 3600

	numerator := m.Minor * seconds
	// Integer division truncates towards zero, so the remainder carries the sign
	// of the numerator; comparing twice the absolute remainder against the divisor
	// tells us whether to round away from zero.
	quotient := numerator / secondsPerHour
	remainder := numerator % secondsPerHour
	if remainder < 0 {
		remainder = -remainder
	}
	if remainder*2 >= secondsPerHour {
		if numerator < 0 {
			quotient--
		} else {
			quotient++
		}
	}
	return Money{Minor: quotient, Currency: m.Currency}
}

// ApplyPercent returns the amount increased by pct percent, used for the markup on
// a billable expense. Rounding is half away from zero, as above.
func (m Money) ApplyPercent(pct int64) Money {
	numerator := m.Minor * pct
	quotient := numerator / 100
	remainder := numerator % 100
	if remainder < 0 {
		remainder = -remainder
	}
	if remainder*2 >= 100 {
		if numerator < 0 {
			quotient--
		} else {
			quotient++
		}
	}
	return Money{Minor: m.Minor + quotient, Currency: m.Currency}
}

// ScalePercent returns the amount taken *as* pct percent of itself: 150 gives one
// and a half times the amount, 50 gives half of it.
//
// Deliberately separate from ApplyPercent, which *adds* pct. The two read almost
// identically at a call site and mean very different things - a 150% overtime
// rate is one and a half times the base, while a 150% expense markup is two and
// a half times the cost - so they are named for what they do rather than sharing
// one function with a flag.
func (m Money) ScalePercent(pct int64) Money {
	numerator := m.Minor * pct
	quotient := numerator / 100
	remainder := numerator % 100
	if remainder < 0 {
		remainder = -remainder
	}
	if remainder*2 >= 100 {
		if numerator < 0 {
			quotient--
		} else {
			quotient++
		}
	}
	return Money{Minor: quotient, Currency: m.Currency}
}

// String renders the amount with its currency's own number of decimal places and
// its code, e.g. "1234.50 SEK" or "1234 JPY". Display formatting for a locale is
// a presentation concern and belongs in the web layer; this is for logs, exports
// and debugging.
func (m Money) String() string {
	if m.Currency == "" {
		return m.Decimal()
	}
	return m.Decimal() + " " + m.Currency
}

// Decimal renders just the numeric part, e.g. "1234.50", for CSV and JSON export
// where the currency travels in its own column or field.
//
// As many decimal places as the currency has minor units, which for a yen is
// none: "1234.00 JPY" is not a smaller amount than "1234 JPY", it is a figure
// nobody writing in yen would recognise, and a spreadsheet column that mixes
// the two cannot be summed.
func (m Money) Decimal() string {
	sign := ""
	minor := m.Minor
	if minor < 0 {
		sign = "-"
		minor = -minor
	}

	scale := minorUnitsPer(m.Currency)
	whole := strconv.FormatInt(minor/scale, 10)
	if scale == 1 {
		return sign + whole
	}
	exponent := CurrencyExponent(m.Currency)
	return sign + whole + "." +
		fmt.Sprintf("%0*d", exponent, minor%scale)
}

// ParseMoney reads a human-entered amount such as "1234.50", "1234,50" or "1 234"
// into minor units. Both the comma and the point are accepted as the decimal
// separator because this application is used across locales, and spaces (including
// the non-breaking space some spreadsheets emit) are stripped as thousands
// separators.
//
// How many minor units a decimal place is worth depends on the currency: "100"
// in yen is a hundred minor units and in euro ten thousand of them. Parsing
// every currency at two decimals - which this did - meant a yen instance held
// every figure at a hundred times what was typed.
func ParseMoney(s, currency string) (Money, error) {
	cleaned := strings.NewReplacer(" ", "", " ", "", " ", "", ",", ".").Replace(strings.TrimSpace(s))
	if cleaned == "" {
		return Money{Currency: strings.ToUpper(currency)}, nil
	}

	negative := strings.HasPrefix(cleaned, "-")
	cleaned = strings.TrimPrefix(cleaned, "-")

	major, frac, hasFrac := strings.Cut(cleaned, ".")
	if major == "" {
		major = "0"
	}
	majorVal, err := strconv.ParseInt(major, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("invalid amount %q", s)
	}

	exponent := CurrencyExponent(currency)
	var fracVal int64
	if hasFrac && exponent > 0 {
		// Pad or truncate to exactly the currency's number of digits: in euro
		// "5" means 50 minor units, and "505" is more precision than a minor
		// unit can hold, so it is truncated.
		for len(frac) < exponent {
			frac += "0"
		}
		if len(frac) > exponent {
			frac = frac[:exponent]
		}
		if fracVal, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return Money{}, fmt.Errorf("invalid amount %q", s)
		}
	} else if hasFrac && frac != "" {
		// A currency with no minor unit, given decimals anyway - "100.5" yen.
		// Truncated rather than refused, which is what the extra digits of a
		// two-decimal currency already get, and refusing would turn a harmless
		// paste from a spreadsheet into an error on a form.
		if _, err := strconv.ParseInt(frac, 10, 64); err != nil {
			return Money{}, fmt.Errorf("invalid amount %q", s)
		}
	}

	minor := majorVal*minorUnitsPer(currency) + fracVal
	if negative {
		minor = -minor
	}
	return Money{Minor: minor, Currency: strings.ToUpper(strings.TrimSpace(currency))}, nil
}
