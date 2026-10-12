package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Durations are whole seconds throughout the application, held as int64.
//
// Seconds rather than minutes because a timer records a real start and stop, and
// truncating at capture throws away information we cannot get back. Integers
// rather than a float of hours for the reasons in
// docs/adr/0014-exact-money-and-duration.md.

// RoundingMode says which way a duration is moved when it is rounded to a billing
// increment.
type RoundingMode string

const (
	// RoundUp always moves up to the next increment. The common consultancy rule:
	// a 3-minute call is billed as 15 minutes.
	RoundUp RoundingMode = "up"
	// RoundNearest moves to the closest increment, ties going up.
	RoundNearest RoundingMode = "nearest"
	// RoundDown truncates to the increment. Rare, and generous to the client.
	RoundDown RoundingMode = "down"
	// RoundNone bills the exact recorded time.
	RoundNone RoundingMode = "none"
)

// RoundingRule describes how a recorded duration becomes a billable duration.
//
// The rule that was applied is stored on each entry when it is billed, so that
// changing the policy later cannot retroactively alter an amount that has already
// been invoiced. See docs/adr/0014-exact-money-and-duration.md.
type RoundingRule struct {
	Mode RoundingMode
	// IncrementSeconds is the billing increment, e.g. 900 for quarter hours.
	IncrementSeconds int64
	// MinimumSeconds is a floor applied to any non-zero duration, e.g. a
	// one-hour minimum call-out charge.
	MinimumSeconds int64
}

// NoRounding bills exactly what was recorded. It is the default so that a user who
// never configures anything is never surprised by a number they did not record.
var NoRounding = RoundingRule{Mode: RoundNone}

// NamedRoundingRules are the presets offered in the interface.
//
// A minimum with no increment is a common consultancy arrangement - a call-out
// or an on-site visit is billed as a whole block however long it actually took -
// and it is expressed here as a minimum with rounding otherwise disabled, so a
// six-hour visit under a four-hour minimum still bills six hours rather than
// being rounded up to a block boundary.
//
// The keys are the serialised form, so they are what ends up stored on an entry
// and must not change once they have been used.
var NamedRoundingRules = []struct {
	// Key is the stored value, e.g. "up/900/0".
	Key string
	// MessageKey names the translated label in the message catalogue.
	MessageKey string
}{
	{Key: "none", MessageKey: "rounding.none"},
	{Key: "up/900/0", MessageKey: "rounding.up15"},
	{Key: "up/1800/0", MessageKey: "rounding.up30"},
	{Key: "up/3600/0", MessageKey: "rounding.up60"},
	{Key: "nearest/900/0", MessageKey: "rounding.nearest15"},
	{Key: "nearest/1800/0", MessageKey: "rounding.nearest30"},
	// Minimums: bill at least this much for any work at all.
	{Key: "none/0/7200", MessageKey: "rounding.min2h"},
	{Key: "none/0/14400", MessageKey: "rounding.min4h"},
	{Key: "none/0/28800", MessageKey: "rounding.min8h"},
	// A minimum combined with an increment, which is the most common real
	// arrangement: at least two hours, and quarter hours after that.
	{Key: "up/900/7200", MessageKey: "rounding.min2h15"},
	{Key: "up/900/14400", MessageKey: "rounding.min4h15"},
}

// String renders the rule compactly for storage on an entry and for display, e.g.
// "up/900/3600". This is what gets persisted, so it must round-trip.
func (r RoundingRule) String() string {
	if r.Mode == "" && r.MinimumSeconds == 0 {
		return string(RoundNone)
	}
	if r.Mode == RoundNone && r.MinimumSeconds == 0 {
		return string(RoundNone)
	}
	mode := r.Mode
	if mode == "" {
		mode = RoundNone
	}
	return fmt.Sprintf("%s/%d/%d", mode, r.IncrementSeconds, r.MinimumSeconds)
}

// ParseRoundingRule reads back what String wrote. An unrecognised value degrades
// to no rounding rather than failing: a historical entry with a rule we no longer
// understand should still be readable, and billing it as recorded is the
// conservative choice.
func ParseRoundingRule(s string) RoundingRule {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return NoRounding
	}
	mode := RoundingMode(parts[0])
	switch mode {
	case RoundUp, RoundNearest, RoundDown:
	case RoundNone:
		// "none/0/7200" is a minimum with no increment - bill at least two
		// hours, and the exact recorded time beyond that. A legitimate and
		// common arrangement, so it must round-trip rather than degrading.
	default:
		return NoRounding
	}
	inc, err1 := strconv.ParseInt(parts[1], 10, 64)
	min, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return NoRounding
	}
	return RoundingRule{Mode: mode, IncrementSeconds: inc, MinimumSeconds: min}
}

// Apply converts a recorded duration into a billable one.
//
// This is the single point at which rounding happens in the whole application.
// Totals are the sum of already-rounded per-entry durations, never a rounding of
// a sum: the two differ, and only the former lets a client reconcile an invoice
// line by line.
func (r RoundingRule) Apply(seconds int64) int64 {
	if seconds <= 0 {
		// A zero or negative duration is never inflated to a minimum. A minimum
		// applies to work that happened, not to an absence of work.
		return 0
	}

	rounded := seconds
	if r.IncrementSeconds > 0 {
		switch r.Mode {
		case RoundUp:
			// Ceiling division: add increment-1 before truncating.
			rounded = ((seconds + r.IncrementSeconds - 1) / r.IncrementSeconds) * r.IncrementSeconds
		case RoundNearest:
			// Ties go up, matching the invoice convention in Money.MulDurationHours.
			rounded = ((seconds + r.IncrementSeconds/2) / r.IncrementSeconds) * r.IncrementSeconds
		case RoundDown:
			rounded = (seconds / r.IncrementSeconds) * r.IncrementSeconds
			// Rounding down must not erase work entirely; a 5-minute entry under a
			// 15-minute increment would otherwise become zero and vanish from the
			// invoice without anyone noticing.
			if rounded == 0 {
				rounded = r.IncrementSeconds
			}
		}
	}

	if r.MinimumSeconds > 0 && rounded < r.MinimumSeconds {
		rounded = r.MinimumSeconds
	}
	return rounded
}

// FormatDuration renders seconds as "1h 30m", the form used throughout the UI.
// Seconds are shown only for durations under a minute, where "0m" would look like
// nothing was recorded.
func FormatDuration(seconds int64) string {
	if seconds < 0 {
		return "-" + FormatDuration(-seconds)
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	if hours == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %02dm", hours, minutes)
}

// FormatDecimalHours renders seconds as decimal hours to two places ("1.50"),
// which is what CSV exports and most invoicing systems expect.
func FormatDecimalHours(seconds int64) string {
	// Compute in hundredths of an hour with integer arithmetic so the exported
	// figure never drifts: 5400 seconds is exactly 1.50, not 1.4999999.
	hundredths := (seconds*100 + 1800) / 3600
	return fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100)
}

// ParseDuration reads the several ways a human types a duration into seconds.
//
// Accepted: "1.5", "1,5" (decimal hours - what people copy out of a spreadsheet),
// "1h30", "1h30m", "90m", "45s", "1:30" (hours:minutes), and bare integers, which
// are read as minutes because "30" almost always means half an hour rather than
// thirty hours.
func ParseDuration(s string) (int64, error) {
	in := strings.ToLower(strings.TrimSpace(s))
	in = strings.ReplaceAll(in, " ", "")
	if in == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// "1:30" - hours and minutes, as a clock-like duration.
	if h, m, ok := strings.Cut(in, ":"); ok {
		hours, err1 := strconv.ParseInt(h, 10, 64)
		mins, err2 := strconv.ParseInt(m, 10, 64)
		if err1 != nil || err2 != nil || mins >= 60 || hours < 0 || mins < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return hours*3600 + mins*60, nil
	}

	// Unit-suffixed forms: "1h", "1h30", "1h30m", "90m", "45s".
	if strings.ContainsAny(in, "hms") {
		return parseUnitDuration(in, s)
	}

	// A bare number is decimal hours if it has a fractional part ("1.5"), and
	// minutes if it is a whole number ("30"). This matches how people actually
	// type: nobody means thirty hours.
	normalised := strings.Replace(in, ",", ".", 1)
	if strings.Contains(normalised, ".") {
		return decimalUnits(normalised, 3600, s)
	}
	mins, err := strconv.ParseInt(normalised, 10, 64)
	if err != nil || mins < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return mins * 60, nil
}

// decimalUnits converts a decimal count of some unit into whole seconds.
//
// Integer arithmetic throughout, rather than parsing a float and multiplying.
// The values are exact either way at the sizes involved, but a float in a
// parser is the first step of the drift ADR-0014 exists to prevent, and the
// integer version is no harder to read: 1.5 hours is one lot of 3600 plus five
// tenths of one, and the halving is a rounding decision made in the open.
//
// It replaced the one strconv.ParseFloat in the tree, which internal/repocheck
// had to carry a named exemption for.
func decimalUnits(text string, secondsPerUnit int64, original string) (int64, error) {
	whole, fraction, _ := strings.Cut(text, ".")

	units := int64(0)
	if whole != "" {
		parsed, err := strconv.ParseInt(whole, 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid duration %q", original)
		}
		units = parsed
	}
	seconds := units * secondsPerUnit

	if fraction == "" {
		return seconds, nil
	}
	// A long fraction is somebody's spreadsheet writing 1.3333333333 for a third
	// of an hour. Nine digits is far past the point where another one changes
	// the answer, and it keeps the scale below an int64 overflow.
	if len(fraction) > 9 {
		fraction = fraction[:9]
	}
	numerator, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil || numerator < 0 {
		return 0, fmt.Errorf("invalid duration %q", original)
	}
	denominator := int64(1)
	for range fraction {
		denominator *= 10
	}
	// Half away from zero, the same rounding the money arithmetic uses.
	return seconds + (numerator*secondsPerUnit+denominator/2)/denominator, nil
}

// parseUnitDuration handles the "1h30m" family by walking the string and applying
// each number to the unit that follows it. A trailing number with no unit after an
// hours component is read as minutes, so "1h30" works as people expect.
func parseUnitDuration(in, original string) (int64, error) {
	var total int64
	var current strings.Builder
	sawHours := false

	for _, r := range in {
		switch {
		case r >= '0' && r <= '9':
			current.WriteRune(r)
		case r == 'h' || r == 'm' || r == 's':
			if current.Len() == 0 {
				return 0, fmt.Errorf("invalid duration %q", original)
			}
			n, err := strconv.ParseInt(current.String(), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", original)
			}
			current.Reset()
			switch r {
			case 'h':
				total += n * 3600
				sawHours = true
			case 'm':
				total += n * 60
			case 's':
				total += n
			}
		default:
			return 0, fmt.Errorf("invalid duration %q", original)
		}
	}

	if current.Len() > 0 {
		n, err := strconv.ParseInt(current.String(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", original)
		}
		if !sawHours {
			return 0, fmt.Errorf("invalid duration %q", original)
		}
		total += n * 60
	}
	return total, nil
}

// SecondsBetween returns the whole seconds elapsed between two instants. It is the
// only place durations are derived from timestamps, and it works on the absolute
// instants rather than wall-clock times, so a timer running across a
// daylight-saving transition reports the time that actually passed.
// See docs/adr/0015-utc-storage-local-display.md.
func SecondsBetween(start, end time.Time) int64 {
	return int64(end.Sub(start).Seconds())
}

// DurationUnit is what a bare number means where it was written.
//
// A person typing into the duration box on a form has the whole vocabulary
// available and the placeholder tells them so, which is why ParseDuration can
// read "30" as thirty minutes. A column in somebody's spreadsheet is different:
// the header already said what the numbers are, and reading "2" under a column
// called "hours" as two minutes is not a lenient parse, it is the wrong answer
// arrived at confidently.
type DurationUnit string

const (
	// UnitUnstated is a column whose name does not say - "duration", "time".
	UnitUnstated DurationUnit = ""
	// UnitHours is a column named for hours: "hours", "timmar", "duration_hours".
	UnitHours DurationUnit = "hours"
	// UnitMinutes is a column named for minutes: "minutes", "mins", "minuter".
	UnitMinutes DurationUnit = "minutes"
)

// ErrAmbiguousDuration means a bare number was written under a heading that does
// not say what it counts.
//
// Refused rather than guessed, for the same reason an ambiguous date is
// (ADR-0022): "8" in a column called "duration" is eight hours or eight minutes
// depending on which system exported the file, and a wrong guess silently turns
// a day of work into a coffee break. Refusing names the value and is fixable in
// a text editor; guessing is discovered at the end of the month.
var ErrAmbiguousDuration = errors.New("ambiguous duration")

// ParseDurationIn reads a duration written under a heading that may name its
// unit.
//
// A value carrying its own unit means what it says, whatever the column is
// called: "45m" under a column headed "hours" is three quarters of an hour,
// because the cell is more specific than the header and somebody wrote it
// deliberately.
func ParseDurationIn(s string, unit DurationUnit) (int64, error) {
	in := strings.ToLower(strings.TrimSpace(s))
	in = strings.ReplaceAll(in, " ", "")
	if in == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Anything with a unit or a clock separator in it is self-describing.
	if strings.ContainsAny(in, "hms:") {
		return ParseDuration(s)
	}

	normalised := strings.Replace(in, ",", ".", 1)
	switch unit {
	case UnitHours:
		return decimalUnits(normalised, 3600, s)
	case UnitMinutes:
		return decimalUnits(normalised, 60, s)
	}

	// The column did not say. A fraction is still unambiguous in practice -
	// nobody records one and a half minutes - so only a whole number is refused.
	if strings.Contains(normalised, ".") {
		return decimalUnits(normalised, 3600, s)
	}
	// Only a well-formed number is ambiguous. Anything else is a mistake in the
	// file, and saying "this could be hours or minutes" about "abc" would send
	// somebody looking for the wrong problem.
	if value, err := strconv.ParseInt(normalised, 10, 64); err != nil || value < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return 0, fmt.Errorf("%w: %q could be hours or minutes", ErrAmbiguousDuration, s)
}
