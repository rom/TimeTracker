package domain

import (
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

// String renders the rule compactly for storage on an entry and for display, e.g.
// "up/900/3600". This is what gets persisted, so it must round-trip.
func (r RoundingRule) String() string {
	if r.Mode == "" || r.Mode == RoundNone {
		return string(RoundNone)
	}
	return fmt.Sprintf("%s/%d/%d", r.Mode, r.IncrementSeconds, r.MinimumSeconds)
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
		hours, err := strconv.ParseFloat(normalised, 64)
		if err != nil || hours < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		// The float is confined to this parse of user input and is immediately
		// converted to integer seconds; it never reaches storage or a total.
		return int64(hours*3600 + 0.5), nil
	}
	mins, err := strconv.ParseInt(normalised, 10, 64)
	if err != nil || mins < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return mins * 60, nil
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
