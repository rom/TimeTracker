package i18n

import (
	"fmt"
	"strings"
	"time"
)

// Locale-aware formatting.
//
// Translating strings is only half of localisation. A Swedish reader expects
// "1,50" and "2026-03-16", and showing them "1.50" reads as an error rather
// than as English. These are the conventions this application actually needs:
// decimal numbers, durations, money and dates.

// DecimalSeparator returns the character this locale writes between the whole
// and fractional parts of a number.
func (p *Printer) DecimalSeparator() string {
	switch p.code {
	case "sv":
		// Swedish, like most of continental Europe, uses a comma. This is not
		// cosmetic: "1.5 hours" read by a Swedish speaker as one-and-a-half is
		// a coincidence of it also being a valid decimal, and larger figures
		// become genuinely ambiguous.
		return ","
	default:
		return "."
	}
}

// GroupSeparator returns the thousands separator.
func (p *Printer) GroupSeparator() string {
	switch p.code {
	case "sv":
		// A non-breaking space, which is the Swedish convention and which stops
		// a number wrapping across a line break mid-figure.
		return " "
	default:
		return ","
	}
}

// FormatDecimal renders a string produced with a point - such as "1234.50" from
// the domain layer - in this locale's convention.
//
// It takes the already-formatted string rather than a number, so the exact
// integer arithmetic in the domain layer stays the single source of the value
// and this only changes how it is written
// (docs/adr/0014-exact-money-and-duration.md).
func (p *Printer) FormatDecimal(value string) string {
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")

	whole, fraction, hasFraction := strings.Cut(value, ".")

	// Group the whole part from the right.
	var grouped strings.Builder
	for i, digit := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteString(p.GroupSeparator())
		}
		grouped.WriteRune(digit)
	}

	result := grouped.String()
	if hasFraction {
		result += p.DecimalSeparator() + fraction
	}
	if negative {
		// A true minus sign rather than a hyphen: it aligns with digits and is
		// what a screen reader announces as "minus".
		result = "−" + result
	}
	return result
}

// FormatMoney renders minor units with a currency code, in this locale's
// convention.
func (p *Printer) FormatMoney(minor int64, currency string) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}
	amount := p.FormatDecimal(fmt.Sprintf("%d.%02d", minor/100, minor%100))
	if negative {
		amount = "−" + amount
	}
	if currency == "" {
		return amount
	}
	switch p.code {
	case "sv":
		// Swedish writes the amount then the currency: "1 234,50 SEK".
		return amount + " " + currency
	default:
		return amount + " " + currency
	}
}

// FormatDuration renders seconds as hours and minutes, translated.
//
// Both languages abbreviate, but not identically: Swedish writes "1 tim 30 min"
// where English writes "1h 30m". The unit labels come from the catalogue so a
// third language can differ again.
func (p *Printer) FormatDuration(seconds int64) string {
	if seconds < 0 {
		return "−" + p.FormatDuration(-seconds)
	}
	if seconds < 60 {
		return fmt.Sprintf("%d%s", seconds, p.T("unit.seconds"))
	}

	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	if hours == 0 {
		return fmt.Sprintf("%d%s", minutes, p.T("unit.minutes"))
	}
	return fmt.Sprintf("%d%s %02d%s", hours, p.T("unit.hours"), minutes, p.T("unit.minutes"))
}

// FormatDate renders a calendar date.
//
// The default, for both languages, is ISO 8601 (2026-03-16): the Swedish
// everyday format, the national standard, and unambiguous. English does not get
// a locale-specific order of its own, because 03/04 is genuinely ambiguous
// between British and American readers and a timesheet is not the place for
// that.
//
// An administrator may still override it, because "unambiguous" is not the same
// as "what the accounting department will accept" - and being told the tool is
// right is no help to somebody who has to retype every date by hand.
func (p *Printer) FormatDate(t time.Time) string {
	switch p.formats.Date {
	case "dmy":
		return t.Format("02/01/2006")
	case "mdy":
		return t.Format("01/02/2006")
	default:
		return t.Format("2006-01-02")
	}
}

// FormatDateLong renders a date with the month and weekday named.
func (p *Printer) FormatDateLong(t time.Time) string {
	weekday := p.T("weekday." + strings.ToLower(t.Weekday().String()))
	month := p.T("month." + strings.ToLower(t.Month().String()))

	switch p.code {
	case "sv":
		// "måndag 16 mars 2026", lower case, as Swedish orthography requires.
		return fmt.Sprintf("%s %d %s %d", weekday, t.Day(), month, t.Year())
	default:
		return fmt.Sprintf("%s %d %s %d", weekday, t.Day(), month, t.Year())
	}
}

// FormatWeekday renders an abbreviated weekday name for the week grid.
func (p *Printer) FormatWeekday(t time.Time) string {
	return p.T("weekday.short." + strings.ToLower(t.Weekday().String()))
}

// FormatTime renders a wall-clock time.
//
// Both supported locales use a 24-hour clock by default, which is also what
// avoids am/pm ambiguity on a timesheet - "12:30 to 1:15" is a duration nobody
// should have to think about. An administrator may still ask for 12-hour times,
// because a preference this strong on a screen somebody reads all day is worth
// more than the argument for consistency.
func (p *Printer) FormatTime(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format(p.timeLayout())
}

// FormatClock renders a time with seconds, for the header clock.
func (p *Printer) FormatClock(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	if p.formats.Clock == "12h" {
		return t.In(loc).Format("3:04:05 PM")
	}
	return t.In(loc).Format("15:04:05")
}

// FormatHour renders a whole hour, for the timeline's rules.
func (p *Printer) FormatHour(hour int) string {
	if p.formats.Clock == "12h" {
		suffix := "am"
		display := hour % 24
		if display >= 12 {
			suffix = "pm"
		}
		display %= 12
		if display == 0 {
			display = 12
		}
		return fmt.Sprintf("%d%s", display, suffix)
	}
	return fmt.Sprintf("%02d:00", hour%24)
}

// timeLayout is the reference layout for a wall-clock time.
func (p *Printer) timeLayout() string {
	if p.formats.Clock == "12h" {
		return "3:04 PM"
	}
	return "15:04"
}

// ClockIs12Hour reports the choice to callers that have to render a time
// themselves - the browser-side clock, which ticks without asking the server.
func (p *Printer) ClockIs12Hour() bool { return p.formats.Clock == "12h" }
