package i18n

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
)

// Writing numbers, dates and durations for a reader.
//
// These are not cosmetics. "1.5 hours" read by a Swedish speaker as
// one-and-a-half is a coincidence of it also being a valid decimal; at four
// figures the two conventions disagree outright, and a timesheet that renders
// an invoice total in the wrong one has produced a wrong number on paper.

// TestNumbersAreWrittenTheWayTheReaderReadsThem.
//
// The separators are swapped between the two languages, which is exactly the
// case where getting it half right - the decimal but not the thousands - is
// worse than not translating at all, because 1,234 means two different
// quantities.
func TestNumbersAreWrittenTheWayTheReaderReadsThem(t *testing.T) {
	english := NewPrinter("en")
	swedish := NewPrinter("sv")

	if got := english.DecimalSeparator(); got != "." {
		t.Errorf("English decimal separator = %q, want a point", got)
	}
	if got := swedish.DecimalSeparator(); got != "," {
		t.Errorf("Swedish decimal separator = %q, want a comma", got)
	}
	if english.GroupSeparator() == swedish.GroupSeparator() {
		t.Error("both languages group thousands the same way, which cannot be " +
			"right for a comma-decimal language")
	}
	// A non-breaking space, so a figure does not wrap across a line mid-number.
	if got := swedish.GroupSeparator(); got != " " {
		t.Errorf("Swedish group separator is %q, want a non-breaking space", got)
	}

	cases := []struct {
		value string
		en    string
		sv    string
	}{
		{"0.00", "0.00", "0,00"},
		{"1.50", "1.50", "1,50"},
		{"1234.50", "1,234.50", "1 234,50"},
		{"1234567.89", "1,234,567.89", "1 234 567,89"},
		{"100", "100", "100"},
		{"1000", "1,000", "1 000"},
		{"", "", ""},
	}
	for _, tc := range cases {
		name := tc.value
		if name == "" {
			name = "nothing at all"
		}
		t.Run(name, func(t *testing.T) {
			if got := english.FormatDecimal(tc.value); got != tc.en {
				t.Errorf("English %q = %q, want %q", tc.value, got, tc.en)
			}
			if got := swedish.FormatDecimal(tc.value); got != tc.sv {
				t.Errorf("Swedish %q = %q, want %q", tc.value, got, tc.sv)
			}
		})
	}
}

// TestMoneyIsWrittenWithItsCurrency.
//
// An amount on a screen or an invoice. The minus sign is a real minus rather
// than a hyphen, because a credit note's figure should not look like a
// hyphenated word, and the currency is always shown: "1 234,50" alone is not
// an amount of money.
func TestMoneyIsWrittenWithItsCurrency(t *testing.T) {
	english := NewPrinter("en")
	swedish := NewPrinter("sv")

	cases := []struct {
		minor    int64
		currency string
		en, sv   string
	}{
		{0, "EUR", "0.00 EUR", "0,00\u00a0EUR"},
		{150, "EUR", "1.50 EUR", "1,50\u00a0EUR"},
		{123450, "SEK", "1,234.50 SEK", "1\u00a0234,50\u00a0SEK"},
		{-150, "EUR", "−1.50 EUR", "−1,50\u00a0EUR"},
		{5, "EUR", "0.05 EUR", "0,05\u00a0EUR"},
		// A currency with no minor unit is written without the two decimal
		// places it does not have. The figure comes from domain.Money, which
		// knows how many a currency has; writing it here as well is how this
		// ended up with a second copy of that rule and a wrong one.
		{1234, "JPY", "1,234 JPY", "1\u00a0234\u00a0JPY"},
		{0, "JPY", "0 JPY", "0\u00a0JPY"},
		{-1234, "JPY", "−1,234 JPY", "−1\u00a0234\u00a0JPY"},
		// And one with three writes all three.
		{1234567, "KWD", "1,234.567 KWD", "1\u00a0234,567\u00a0KWD"},
	}
	for _, tc := range cases {
		t.Run(tc.en, func(t *testing.T) {
			if got := english.FormatMoney(tc.minor, tc.currency); got != tc.en {
				t.Errorf("English = %q, want %q", got, tc.en)
			}
			if got := swedish.FormatMoney(tc.minor, tc.currency); got != tc.sv {
				t.Errorf("Swedish = %q, want %q", got, tc.sv)
			}
		})
	}

	// Swedish separates the amount from the currency with a non-breaking space
	// as well, so an amount does not wrap away from its currency code.
	if !strings.Contains(swedish.FormatMoney(150, "EUR"), "\u00a0EUR") {
		t.Error("the Swedish amount and its currency can wrap apart")
	}

	// A minus sign, not a hyphen: they render differently and only one is a
	// minus.
	if strings.Contains(english.FormatMoney(-150, "EUR"), "-") {
		t.Error("a negative amount uses a hyphen rather than a minus sign")
	}

	// With no currency it is just the number, for a column that names the
	// currency in its heading.
	if got := english.FormatMoney(150, ""); got != "1.50" {
		t.Errorf("an amount with no currency = %q, want just the figure", got)
	}
}

// TestDurationsReadAsHoursAndMinutes.
//
// What every total on every screen is rendered with. Both languages
// abbreviate and not identically, so the unit labels come from the catalogue -
// a third language can differ again without touching this code.
func TestDurationsReadAsHoursAndMinutes(t *testing.T) {
	english := NewPrinter("en")
	swedish := NewPrinter("sv")

	cases := []struct {
		seconds int64
		en      string
	}{
		{0, "0s"},
		{45, "45s"},
		{60, "1m"},
		{90, "1m"},
		{1800, "30m"},
		{3600, "1h 00m"},
		{5400, "1h 30m"},
		{36000, "10h 00m"},
		{-5400, "−1h 30m"},
	}
	for _, tc := range cases {
		t.Run(tc.en, func(t *testing.T) {
			if got := english.FormatDuration(tc.seconds); got != tc.en {
				t.Errorf("English %ds = %q, want %q", tc.seconds, got, tc.en)
			}
			// Swedish differs in its labels and agrees on its figures.
			got := swedish.FormatDuration(tc.seconds)
			if got == "" {
				t.Errorf("Swedish %ds rendered as nothing", tc.seconds)
			}
			if tc.seconds >= 3600 && !strings.Contains(got, "1") && !strings.Contains(got, "0") {
				t.Errorf("Swedish %ds = %q, which carries no figure", tc.seconds, got)
			}
		})
	}

	// The two languages must not render an hour identically, or the labels are
	// not coming from the catalogue at all.
	if english.FormatDuration(5400) == swedish.FormatDuration(5400) {
		t.Errorf("both languages render 90 minutes as %q; the unit labels are "+
			"meant to come from the catalogue", english.FormatDuration(5400))
	}
}

// TestDatesDefaultToSomethingUnambiguous.
//
// 03/04 is genuinely ambiguous between a British and an American reader, and a
// timesheet is not the place for that - so both languages default to ISO,
// which is also the Swedish everyday format and the national standard.
//
// An administrator may still override it, because "unambiguous" is not the
// same as "what the accounting department will accept".
func TestDatesDefaultToSomethingUnambiguous(t *testing.T) {
	when := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)

	for _, code := range []string{"en", "sv"} {
		t.Run(code, func(t *testing.T) {
			if got := NewPrinter(code).FormatDate(when); got != "2026-03-04" {
				t.Errorf("%s renders the date as %q, want ISO", code, got)
			}
		})
	}

	overrides := map[string]string{
		"iso":  "2026-03-04",
		"dmy":  "04/03/2026",
		"mdy":  "03/04/2026",
		"":     "2026-03-04",
		"auto": "2026-03-04",
	}
	for format, want := range overrides {
		name := format
		if name == "" {
			name = "no override"
		}
		t.Run(name, func(t *testing.T) {
			printer := NewPrinter("en").WithFormats(Formats{Date: format})
			if got := printer.FormatDate(when); got != want {
				t.Errorf("the %q format renders as %q, want %q", format, got, want)
			}
		})
	}

	// The long form and the month name are translated, which is the point of
	// having them come from the catalogue.
	if NewPrinter("en").FormatDateLong(when) == NewPrinter("sv").FormatDateLong(when) {
		t.Error("both languages render a long date identically")
	}
	if NewPrinter("en").FormatMonth(when) == NewPrinter("sv").FormatMonth(when) {
		t.Error("both languages name the month identically")
	}
	if NewPrinter("en").FormatWeekday(when) == NewPrinter("sv").FormatWeekday(when) {
		t.Error("both languages name the weekday identically")
	}
	for _, code := range []string{"en", "sv"} {
		if month := NewPrinter(code).FormatMonth(when); !strings.Contains(month, "2026") {
			t.Errorf("%s renders the month as %q, which does not say which year", code, month)
		}
	}
}

// TestAClockIsReadTheWayTheReaderReadsIt.
//
// Twenty-four hours by default, because both languages use it and because a
// timesheet full of am and pm is harder to scan. Twelve is an override, and
// the awkward hours are midnight and noon: rendering either as "0" or "12" in
// the wrong half is a mistake somebody notices only after recording the work.
func TestAClockIsReadTheWayTheReaderReadsIt(t *testing.T) {
	printer := NewPrinter("en")
	twelve := printer.WithFormats(Formats{Clock: "12h"})

	if printer.ClockIs12Hour() {
		t.Error("the default clock is twelve-hour")
	}
	if !twelve.ClockIs12Hour() {
		t.Error("a printer set to twelve-hour says it is not")
	}

	cases := []struct {
		hour          int
		twentyFour    string
		twelveHourHas string
	}{
		{0, "00:00", "12"},
		{1, "01:00", "1"},
		{11, "11:00", "11"},
		{12, "12:00", "12"},
		{13, "13:00", "1"},
		{23, "23:00", "11"},
	}
	for _, tc := range cases {
		t.Run(tc.twentyFour, func(t *testing.T) {
			if got := printer.FormatHour(tc.hour); got != tc.twentyFour {
				t.Errorf("hour %d = %q, want %q", tc.hour, got, tc.twentyFour)
			}
			got := twelve.FormatHour(tc.hour)
			if !strings.Contains(got, tc.twelveHourHas) {
				t.Errorf("hour %d on a twelve-hour clock = %q, want it to contain %q",
					tc.hour, got, tc.twelveHourHas)
			}
			if tc.hour >= 12 && !strings.Contains(strings.ToLower(got), "pm") {
				t.Errorf("hour %d = %q, want it marked as the afternoon", tc.hour, got)
			}
			if tc.hour < 12 && !strings.Contains(strings.ToLower(got), "am") {
				t.Errorf("hour %d = %q, want it marked as the morning", tc.hour, got)
			}
		})
	}

	// A time is rendered in the zone it is asked for, not the one it is
	// stored in - every instant is stored as UTC.
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}
	when := time.Date(2026, 3, 16, 8, 30, 0, 0, time.UTC)
	if got := printer.FormatTime(when, time.UTC); !strings.Contains(got, "08:30") {
		t.Errorf("08:30 UTC rendered in UTC as %q", got)
	}
	if got := printer.FormatTime(when, stockholm); !strings.Contains(got, "09:30") {
		t.Errorf("08:30 UTC rendered in Stockholm as %q, want 09:30", got)
	}
	// A nil location must not panic: it is what an unset preference reaches
	// this with.
	if got := printer.FormatTime(when, nil); got == "" {
		t.Error("a nil location rendered as nothing")
	}
	if got := printer.FormatClock(when, nil); got == "" {
		t.Error("a nil location rendered no clock")
	}
}

// TestNegotiatingALanguageReadsTheWeights.
//
// Browsers commonly send "en-GB,en;q=0.9,sv;q=0.8", and a naive reading of
// the first tag would ignore somebody whose preferred language is listed
// second with a higher weight. Anything unsupported falls back rather than
// producing a half-translated screen.
func TestNegotiatingALanguageReadsTheWeights(t *testing.T) {
	cases := map[string]string{
		"":                        DefaultLanguage,
		"en":                      "en",
		"sv":                      "sv",
		"en-GB":                   "en",
		"sv-SE":                   "sv",
		"sv_SE":                   "sv",
		"  SV  ":                  "sv",
		"en-GB,en;q=0.9,sv;q=0.8": "en",
		"en;q=0.5,sv;q=0.9":       "sv",
		"sv;q=0.1,en;q=0.2":       "en",
		"de,fr":                   DefaultLanguage,
		"de;q=0.9,sv;q=0.1":       "sv",
		"*":                       DefaultLanguage,
		"en,":                     "en",
		"en;q=notanumber":         "en",
		"klingon":                 DefaultLanguage,
		// Equal weights keep the header's own order, which is what the
		// specification intends.
		"sv,en": "sv",
		"en,sv": "en",
	}
	for header, want := range cases {
		name := header
		if strings.TrimSpace(name) == "" {
			name = "an empty header"
		}
		t.Run(name, func(t *testing.T) {
			if got := Negotiate(header); got != want {
				t.Errorf("Negotiate(%q) = %q, want %q", header, got, want)
			}
		})
	}
}

// TestTheCataloguesAgreeOnTheirKeys.
//
// A language that quietly lacks a key falls back to English in a place nobody
// noticed, which is how a half-translated interface ships. Both catalogues are
// embedded, so this is checkable rather than a matter of discipline.
func TestTheCataloguesAgreeOnTheirKeys(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatalf("the embedded catalogues did not load: %v", err)
	}

	languages := Languages()
	if len(languages) < 2 {
		t.Fatalf("%d languages are available, want at least two to compare", len(languages))
	}

	reference := Keys(DefaultLanguage)
	if len(reference) == 0 {
		t.Fatal("the default catalogue has no keys")
	}

	for _, language := range languages {
		if language.Code == DefaultLanguage {
			continue
		}
		have := map[string]bool{}
		for _, key := range Keys(language.Code) {
			have[key] = true
		}
		var missing []string
		for _, key := range reference {
			if !have[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s is missing %d keys, starting with %v",
				language.Code, len(missing), missing[:min(5, len(missing))])
		}
	}

	// Every language says what it is called, in its own language, or a
	// language picker is a list of codes.
	for _, language := range languages {
		if language.Name == "" {
			t.Errorf("the language %q has no name", language.Code)
		}
		if !Supported(language.Code) {
			t.Errorf("the language %q is listed and not supported", language.Code)
		}
	}
	if Supported("klingon") || Supported("") {
		t.Error("an unknown code is reported as supported")
	}
}

// TestALookupOfSomethingMissing.
//
// A key that is not in the catalogue has to be visible as such rather than
// rendering as nothing: a blank label on a button is a bug nobody can report,
// and the key itself is a usable description of what is wrong.
func TestALookupOfSomethingMissing(t *testing.T) {
	printer := NewPrinter("en")

	if got := printer.T("no.such.key.exists"); got == "" {
		t.Error("a missing key rendered as nothing, so a button would be blank")
	}
	if !printer.Missing("no.such.key.exists") {
		t.Error("a missing key is not reported as missing")
	}
	if printer.Missing("unit.hours") {
		t.Error("a key that exists is reported as missing")
	}

	// An unknown language falls back rather than producing an empty interface.
	fallback := NewPrinter("klingon")
	if got := fallback.T("unit.hours"); got == "" {
		t.Error("an unknown language renders nothing at all")
	}
	if fallback.Code() != DefaultLanguage {
		t.Errorf("an unknown language reports itself as %q, want the default",
			fallback.Code())
	}

	// A plural always carries its count, since "entries" without a number is
	// not a thing a screen can say.
	for _, count := range []int{0, 1, 2, 100} {
		got := printer.N("entries.count", count)
		if got == "" {
			t.Errorf("a plural of %d rendered as nothing", count)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestSwedishWritesItsCalendarInLowerCase.
//
// Swedish does not capitalise the names of weekdays or months. They are common
// nouns: "måndag 12 oktober", never "Måndag 12 Oktober". FormatDateLong's own
// comment said so - "lower case, as Swedish orthography requires" - while the
// catalogue it reads from delivered "Måndag", so every long date on every
// Swedish screen was written wrong, and so was the weekday list in the routine
// editor and the week-start option in settings.
//
// Asserted as the rule rather than as fourteen expected strings, so a third
// language that shares the rule can be added to the list below and a language
// that does not is left alone.
func TestSwedishWritesItsCalendarInLowerCase(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatalf("the embedded catalogues did not load: %v", err)
	}

	// The keys that hold a name, listed rather than guessed at by prefix: the
	// month.* family also holds headings and sentences ("Veckosumma"), which
	// are capitalised like any other.
	var names []string
	for _, weekday := range []string{"monday", "tuesday", "wednesday", "thursday",
		"friday", "saturday", "sunday"} {
		names = append(names, "weekday."+weekday, "weekday.short."+weekday)
	}
	for _, month := range []string{"january", "february", "march", "april", "may",
		"june", "july", "august", "september", "october", "november", "december"} {
		names = append(names, "month."+month)
	}
	for number := 1; number <= 7; number++ {
		names = append(names, fmt.Sprintf("weekday.short.%d", number))
	}

	printer := NewPrinter("sv")
	for _, key := range names {
		value := printer.T(key)
		if value == key || value == "" {
			t.Errorf("%s is missing from the Swedish catalogue", key)
			continue
		}
		if first := []rune(value)[0]; first != unicode.ToLower(first) {
			t.Errorf("%s = %q; Swedish writes a weekday or a month in lower case",
				key, value)
		}
	}

	// And the rendered date, which is what somebody actually sees.
	when := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if got := printer.FormatDateLong(when); got != "måndag 12 oktober 2026" {
		t.Errorf("FormatDateLong = %q, want the Swedish spelling", got)
	}
	if got := printer.FormatMonth(when); got != "oktober 2026" {
		t.Errorf("FormatMonth = %q, want the Swedish spelling", got)
	}

	// English capitalises both, and must not have been swept along with it.
	english := NewPrinter("en")
	if got := english.FormatDateLong(when); got != "Monday 12 October 2026" {
		t.Errorf("English FormatDateLong = %q, want the English spelling", got)
	}
}
