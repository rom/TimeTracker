package domain

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// Properties rather than examples.
//
// The tables elsewhere in this package pin specific answers, which is the right
// way to test a rounding boundary: the answer is a fact somebody decided. What a
// table cannot say is "and this holds for every input", and several of the rules
// here are only worth anything if they do.
//
// The inputs are enumerated rather than randomised. A failing seed that has to
// be reproduced from a log line is a worse debugging experience than a loop
// somebody can read, and the interesting ranges here are small enough to walk.

// TestDecimalHoursAreExact.
//
// ParseDuration is the one function in the tree allowed to touch a float: "1.5"
// is what most people type for an hour and a half, and refusing the form would
// be refusing what they reach for first. The exemption is written down in
// internal/repocheck, and this is what makes it safe rather than merely stated -
// every value it can produce is the exact whole number of seconds.
//
// The dangerous inputs are the ones with no exact binary representation. 7.7 is
// 27719.999... seconds before rounding, and a version that truncated instead of
// rounding would bill 27719 - an hour of work short by a second, which nobody
// notices and which makes a timesheet stop adding up to the day.
func TestDecimalHoursAreExact(t *testing.T) {
	for tenths := 1; tenths <= 240; tenths++ {
		hours := float64(tenths) / 10
		text := fmt.Sprintf("%.1f", hours)

		seconds, err := ParseDuration(text)
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", text, err)
		}
		// Computed in integers, which is the answer the float arithmetic has to
		// agree with: a tenth of an hour is exactly 360 seconds.
		if want := int64(tenths) * 360; seconds != want {
			t.Errorf("ParseDuration(%q) = %d, want %d", text, seconds, want)
		}
	}

	// Two decimal places, where the representation error is larger.
	for hundredths := 1; hundredths <= 200; hundredths++ {
		text := fmt.Sprintf("%.2f", float64(hundredths)/100)
		seconds, err := ParseDuration(text)
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", text, err)
		}
		want := int64(math.Round(float64(hundredths) * 36))
		if seconds != want {
			t.Errorf("ParseDuration(%q) = %d, want %d", text, seconds, want)
		}
	}
}

// TestTheDurationFormsAgree.
//
// Five ways to write the same hour and a half, because people type what they
// think in. They have to mean the same number of seconds, or the same day's work
// is a different total depending on who recorded it.
func TestTheDurationFormsAgree(t *testing.T) {
	for _, group := range []struct {
		seconds int64
		forms   []string
	}{
		{5400, []string{"1h30", "1h30m", "90m", "1:30", "1.5", "1,5", " 1 h 30 m "}},
		{3600, []string{"1h", "60m", "1:00", "1.0", "3600s"}},
		{1800, []string{"30m", "0:30", "0.5", "30"}},
		{45, []string{"45s"}},
		{0, []string{"0m", "0:00", "0h"}},
	} {
		for _, form := range group.forms {
			seconds, err := ParseDuration(form)
			if err != nil {
				t.Errorf("ParseDuration(%q): %v", form, err)
				continue
			}
			if seconds != group.seconds {
				t.Errorf("ParseDuration(%q) = %d, want %d", form, seconds, group.seconds)
			}
		}
	}

	// A bare whole number is minutes, because "30" almost always means half an
	// hour. This is the one place where a reasonable person could expect the
	// other answer, so it is pinned rather than left to the reader.
	if seconds, _ := ParseDuration("30"); seconds != 1800 {
		t.Errorf("a bare 30 parsed as %d seconds, want 30 minutes", seconds)
	}
	if seconds, _ := ParseDuration("1.5"); seconds != 5400 {
		t.Errorf("a bare 1.5 parsed as %d seconds, want an hour and a half", seconds)
	}

	// Repeated units accumulate rather than being refused: "1h2h3h4" is
	// 1h + 2h + 3h + 4m. Nobody types that on purpose, and the parser is lenient
	// rather than clever about it - pinned here because it is the sort of
	// behaviour a reader would guess either way.
	if seconds, err := ParseDuration("1h2h3h4"); err != nil || seconds != 21840 {
		t.Errorf("ParseDuration(\"1h2h3h4\") = %d, %v; want the sum, 21840", seconds, err)
	}
}

// TestNonsenseDurationsAreRefused.
//
// Every one of these is something a form will receive, and the answer has to be
// an error rather than a plausible number - a duration silently read as zero is
// a day's work that vanishes.
func TestNonsenseDurationsAreRefused(t *testing.T) {
	for _, input := range []string{
		"", "  ", "abc", "-1h", "-30", "1:99", "1:60", "h", "m",
		"1..5", "--", "1:-30", "∞",
	} {
		if seconds, err := ParseDuration(input); err == nil {
			t.Errorf("ParseDuration(%q) was accepted as %d seconds", input, seconds)
		}
	}
}

// TestRoundingNeverMovesAValueFurtherThanTheIncrement.
//
// The property that makes a rounding rule an adjustment rather than a rewrite.
// Somebody checking an invoice against their own notes tolerates a quarter of an
// hour they were told about; they do not tolerate an hour appearing from
// nowhere, and a mistake in the arithmetic - a multiply where a divide belongs -
// shows up here whatever the increment.
func TestRoundingNeverMovesAValueFurtherThanTheIncrement(t *testing.T) {
	for _, mode := range []RoundingMode{RoundUp, RoundDown, RoundNearest} {
		for _, increment := range []int64{60, 300, 900, 1800, 3600} {
			rule := RoundingRule{Mode: mode, IncrementSeconds: increment}
			for seconds := int64(0); seconds <= 7200; seconds += 7 {
				rounded := rule.Apply(seconds)

				if difference := rounded - seconds; difference >= increment || difference <= -increment {
					t.Fatalf("%s/%d moved %d to %d, which is a whole increment away",
						mode, increment, seconds, rounded)
				}
				// Rounding to nothing. Two of the three modes never do it:
				// "up" cannot, and "down" has an explicit floor because a
				// five-minute entry under a quarter-hour increment would
				// otherwise vanish from an invoice with nobody noticing.
				//
				// "nearest" has no such floor, and does round a short entry to
				// zero - which is arithmetically what "nearest" means and is
				// the same disappearance the "down" comment argues against.
				// Pinned here as the current behaviour rather than asserted as
				// correct: changing it would change what is billed, which is
				// not a decision for a test.
				if seconds > 0 && rounded == 0 && mode != RoundNearest {
					t.Fatalf("%s/%d erased %d seconds of work", mode, increment, seconds)
				}
				if rounded%increment != 0 {
					t.Fatalf("%s/%d produced %d, which is not a multiple of the increment",
						mode, increment, rounded)
				}
				switch mode {
				case RoundUp:
					if rounded < seconds {
						t.Fatalf("up/%d rounded %d down to %d", increment, seconds, rounded)
					}
				case RoundDown:
					// One documented exception: rounding down must not erase
					// work entirely, so anything under a single increment
					// becomes one increment rather than vanishing from the
					// invoice. Everything above that goes down.
					// The floor case: anything under a single increment becomes
					// one increment. Everything else must go down.
					floored := seconds > 0 && seconds < increment && rounded == increment
					if rounded > seconds && !floored {
						t.Fatalf("down/%d rounded %d up to %d", increment, seconds, rounded)
					}
				case RoundNearest:
					if difference := rounded - seconds; difference > increment/2 || difference < -increment/2 {
						t.Fatalf("nearest/%d moved %d to %d, further than half an increment",
							increment, seconds, rounded)
					}
				}
			}
		}
	}
}

// TestRoundingIsIdempotent.
//
// An already-rounded value must not move again, or a duration that passed
// through the rule twice would be billed higher than one that passed through
// once - and which entries had been through it twice would depend on the code
// path rather than on the work.
//
// It holds for the increment, which is what this asserts. It deliberately does
// *not* hold when a minimum is configured, and cannot: a one-hour minimum under
// a nearest-quarter-hour rule turns thirteen seconds into an hour, and an hour
// is already a multiple of the increment, so the second pass is a no-op - but
// under a *nearest-hour* rule a thirty-minute minimum rounds up to an hour on
// the second pass. That is why rounding happens exactly once in the whole
// application, from the raw duration, at one call site (service/billing.go).
func TestRoundingIsIdempotent(t *testing.T) {
	for _, mode := range []RoundingMode{RoundUp, RoundDown, RoundNearest} {
		for _, increment := range []int64{60, 900, 3600} {
			rule := RoundingRule{Mode: mode, IncrementSeconds: increment}
			for seconds := int64(0); seconds <= 7200; seconds += 13 {
				once := rule.Apply(seconds)
				if twice := rule.Apply(once); twice != once {
					t.Fatalf("%s/%d: rounding %d gave %d, and rounding that gave %d",
						mode, increment, seconds, once, twice)
				}
			}
		}
	}

	// The minimum case, pinned as what it is rather than left as a surprise.
	withMinimum := RoundingRule{Mode: RoundNearest, IncrementSeconds: 3600, MinimumSeconds: 1800}
	once := withMinimum.Apply(13)
	if once != 1800 {
		t.Fatalf("thirteen seconds under a thirty-minute minimum came to %d", once)
	}
	if twice := withMinimum.Apply(once); twice == once {
		t.Error("this rule is now idempotent; if that was deliberate, the comment " +
			"above and the single-call-site argument need rewriting")
	}
}

// TestAMinimumAppliesOnlyToWorkThatHappened.
//
// A one-hour call-out minimum is a charge for being called out. Applying it to
// zero would invoice an hour for a day nobody worked, which is the one input
// where the rule's intent and its arithmetic disagree.
func TestAMinimumAppliesOnlyToWorkThatHappened(t *testing.T) {
	rule := RoundingRule{Mode: RoundUp, IncrementSeconds: 900, MinimumSeconds: 3600}

	if rounded := rule.Apply(0); rounded != 0 {
		t.Errorf("a minimum charge was applied to nothing: %d", rounded)
	}
	if rounded := rule.Apply(60); rounded != 3600 {
		t.Errorf("a minute of work rounded to %d, want the one-hour minimum", rounded)
	}
	if rounded := rule.Apply(7200); rounded != 7200 {
		t.Errorf("two hours were reduced to %d by a one-hour minimum", rounded)
	}
}

// TestARoundingRuleSurvivesBeingWrittenDown.
//
// Rules are stored as text on the project row, so the string form is a storage
// format rather than a display convenience: a rule that does not survive the
// round trip is a project quietly billing by a different rule after a restart.
func TestARoundingRuleSurvivesBeingWrittenDown(t *testing.T) {
	for _, rule := range []RoundingRule{
		NoRounding,
		{Mode: RoundUp, IncrementSeconds: 900},
		{Mode: RoundDown, IncrementSeconds: 300, MinimumSeconds: 900},
		{Mode: RoundNearest, IncrementSeconds: 3600, MinimumSeconds: 3600},
	} {
		written := rule.String()
		read := ParseRoundingRule(written)
		if read.Apply(1000) != rule.Apply(1000) || read.Apply(4000) != rule.Apply(4000) {
			t.Errorf("%q came back as a rule that bills differently: %+v vs %+v",
				written, read, rule)
		}
	}

	// Anything unparseable is no rounding rather than a guess: billing by a rule
	// nobody wrote is worse than billing by the clock.
	for _, malformed := range []string{"", "up", "up/900", "up/900/0/extra",
		"sideways/900/0", "up/abc/0", "up/900/abc"} {
		if got := ParseRoundingRule(malformed).Apply(1000); got != 1000 {
			t.Errorf("ParseRoundingRule(%q) invented a rule that turned 1000 into %d",
				malformed, got)
		}
	}
}

// TestMoneyRoundTripsThroughItsDecimalForm.
//
// The decimal form is what goes into a CSV and what comes back from a form
// field, so the pair has to be lossless - including for the values people
// actually type badly: a comma for a point, spaces as thousands separators, and
// the non-breaking space some spreadsheets emit.
func TestMoneyRoundTripsThroughItsDecimalForm(t *testing.T) {
	for _, minor := range []int64{0, 1, 99, 100, 12345, 100000, 999999999, -1, -12345} {
		amount := NewMoney(minor, "SEK")
		parsed, err := ParseMoney(amount.Decimal(), "SEK")
		if err != nil {
			t.Errorf("ParseMoney(%q): %v", amount.Decimal(), err)
			continue
		}
		if parsed.Minor != minor {
			t.Errorf("%d minor units became %q and came back as %d",
				minor, amount.Decimal(), parsed.Minor)
		}
	}

	for _, typed := range []struct {
		text  string
		minor int64
	}{
		{"1234.50", 123450},
		{"1234,50", 123450},
		{"1 234,50", 123450},
		{"1 234,50", 123450}, // a non-breaking space, from a spreadsheet
		{"1234", 123400},
		{"0.05", 5},
		{"-12.34", -1234},
		{"", 0},
	} {
		parsed, err := ParseMoney(typed.text, "SEK")
		if err != nil {
			t.Errorf("ParseMoney(%q): %v", typed.text, err)
			continue
		}
		if parsed.Minor != typed.minor {
			t.Errorf("ParseMoney(%q) = %d, want %d", typed.text, parsed.Minor, typed.minor)
		}
	}
}

// TestMoneyArithmeticIsExactAndCurrencySafe.
//
// Addition and subtraction are inverses, a zero value adopts whatever it is
// added to so a running total can start from nothing, and two currencies refuse
// to combine - because the alternative is a total that is a number with no
// meaning.
func TestMoneyArithmeticIsExactAndCurrencySafe(t *testing.T) {
	for _, minor := range []int64{0, 1, 99, 100, 250000, -4200} {
		amount := NewMoney(minor, "EUR")
		sum, err := amount.Add(NewMoney(12345, "EUR"))
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		back, err := sum.Sub(NewMoney(12345, "EUR"))
		if err != nil {
			t.Fatalf("subtract: %v", err)
		}
		if back.Minor != minor {
			t.Errorf("%d + 12345 - 12345 = %d", minor, back.Minor)
		}
	}

	// The running-total case: `total, _ = total.Add(x)` starting from Money{}.
	var total Money
	for _, minor := range []int64{100, 250, 375} {
		var err error
		if total, err = total.Add(NewMoney(minor, "SEK")); err != nil {
			t.Fatalf("accumulate: %v", err)
		}
	}
	if total.Minor != 725 || total.Currency != "SEK" {
		t.Errorf("a total started from the zero value came to %v", total)
	}

	if _, err := NewMoney(100, "SEK").Add(NewMoney(100, "EUR")); err == nil {
		t.Error("two currencies were added together")
	}
	if _, err := NewMoney(100, "SEK").Sub(NewMoney(100, "EUR")); err == nil {
		t.Error("one currency was subtracted from another")
	}
	// A *non-zero* amount with no currency is not a neutral element: it is a
	// number somebody lost the currency of, and adopting the other side's would
	// invent a fact.
	if _, err := NewMoney(100, "").Add(NewMoney(100, "EUR")); err == nil {
		t.Error("an amount with no currency was added to euros")
	}
	if NewMoney(0, "SEK").IsZero() != true || NewMoney(1, "SEK").IsZero() {
		t.Error("IsZero does not report zero")
	}
}

// TestMoneyFormatsNegativesAndSmallAmounts.
//
// The two cases a naive formatter gets wrong: a negative amount whose minor part
// is rendered from a negative modulus, and an amount under one major unit that
// loses its leading zero.
func TestMoneyFormatsNegativesAndSmallAmounts(t *testing.T) {
	for _, amount := range []struct {
		minor   int64
		decimal string
		full    string
	}{
		{5, "0.05", "0.05 SEK"},
		{50, "0.50", "0.50 SEK"},
		{-5, "-0.05", "-0.05 SEK"},
		{-123456, "-1234.56", "-1234.56 SEK"},
		{100, "1.00", "1.00 SEK"},
	} {
		money := NewMoney(amount.minor, "SEK")
		if got := money.Decimal(); got != amount.decimal {
			t.Errorf("%d minor units render as %q, want %q", amount.minor, got, amount.decimal)
		}
		if got := money.String(); got != amount.full {
			t.Errorf("%d minor units render as %q, want %q", amount.minor, got, amount.full)
		}
	}
}

// TestAnEntryCountsOnlyWhenSomebodyHasAgreedItHappened.
//
// The rule every aggregation in the application goes through, asserted here at
// the source rather than only at the places that use it.
func TestAnEntryCountsOnlyWhenSomebodyHasAgreedItHappened(t *testing.T) {
	for _, entry := range []struct {
		name  string
		entry TimeEntry
		want  bool
	}{
		{"confirmed", TimeEntry{Status: StatusConfirmed}, true},
		{"a proposal nobody has accepted", TimeEntry{Status: StatusPending}, false},
		{"rejected", TimeEntry{Status: StatusRejected}, false},
		{"confirmed but flagged for review", TimeEntry{Status: StatusConfirmed, Flagged: true}, false},
	} {
		if got := entry.entry.Counts(); got != entry.want {
			t.Errorf("%s counts = %v, want %v", entry.name, got, entry.want)
		}
	}

	// Proxy authorship is about who typed it, not about whether it counts: an
	// accepted proposal counts exactly like anything else.
	accepted := TimeEntry{Status: StatusConfirmed, UserID: 1, EnteredBy: 2}
	if !accepted.IsProxy() {
		t.Error("an entry recorded by somebody else is not marked as a proxy")
	}
	if !accepted.Counts() {
		t.Error("an accepted proxy entry does not count")
	}
	if (TimeEntry{UserID: 1, EnteredBy: 1}).IsProxy() {
		t.Error("an entry somebody recorded for themselves is marked as a proxy")
	}
	if (TimeEntry{UserID: 1}).IsProxy() {
		t.Error("an entry with no recorded author is marked as a proxy")
	}
}

// TestARunningTimerIsMeasuredAgainstNow.
//
// The day screen shows a total that includes work in progress, so a running
// entry's duration is computed rather than stored. The clock-skew case is the
// one worth pinning: a negative elapsed time would render as a timer counting
// backwards.
func TestARunningTimerIsMeasuredAgainstNow(t *testing.T) {
	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	running := TimeEntry{StartedAt: start}

	if got := running.ElapsedSeconds(start.Add(90 * time.Minute)); got != 5400 {
		t.Errorf("a running timer after 90 minutes reports %d seconds", got)
	}
	if got := running.ElapsedSeconds(start.Add(-time.Hour)); got != 0 {
		t.Errorf("a timer started in the future reports %d seconds, want 0", got)
	}

	end := start.Add(time.Hour)
	stopped := TimeEntry{StartedAt: start, EndedAt: &end, DurationSeconds: 3600}
	if got := stopped.ElapsedSeconds(start.Add(10 * time.Hour)); got != 3600 {
		t.Errorf("a stopped entry grew to %d seconds as the clock moved", got)
	}
}

// TestAnEntryBelongsToTheDayItWasWorked.
//
// In the entry's own zone, not the reader's. A Monday evening in Stockholm has
// to stay on Monday when the report is run from New York, or somebody's week has
// hours in it they did not work on those days.
func TestAnEntryBelongsToTheDayItWasWorked(t *testing.T) {
	// 22:30 in Stockholm on Monday is 21:30 UTC, which is 16:30 in New York -
	// still Monday everywhere. An hour later it is Tuesday in Stockholm and
	// still Monday in UTC, which is the case that matters.
	stockholm := TimeEntry{
		StartedAt: time.Date(2026, 3, 16, 23, 30, 0, 0, time.UTC),
		TimeZone:  "Europe/Stockholm",
	}
	day := stockholm.LocalDay()
	if day.Day() != 17 {
		t.Errorf("an entry at 00:30 Stockholm time belongs to day %d, want the 17th",
			day.Day())
	}
	if day.Hour() != 0 || day.Minute() != 0 {
		t.Errorf("LocalDay did not truncate to midnight: %v", day)
	}

	// An unknown or empty zone falls back to UTC rather than failing: a stock
	// Windows machine has no system zoneinfo, and a date calculation that
	// errored there would break on exactly one platform.
	for _, zone := range []string{"", "Mars/Olympus_Mons"} {
		entry := TimeEntry{StartedAt: time.Date(2026, 3, 16, 23, 30, 0, 0, time.UTC), TimeZone: zone}
		if got := entry.LocalDay().Day(); got != 16 {
			t.Errorf("zone %q produced day %d, want the UTC fallback's 16th", zone, got)
		}
	}
}

// TestAnAssignmentsLabelIsWhatSomebodyWouldSayOutLoud.
//
// It is used in pickers, in exports and by the quick-add matcher, so it has to
// degrade sensibly when the joins that populate the names were not run - a label
// reading " / / Development" in an export is the kind of thing a client asks
// about.
func TestAnAssignmentsLabelIsWhatSomebodyWouldSayOutLoud(t *testing.T) {
	for _, assignment := range []struct {
		name       string
		assignment Assignment
		want       string
	}{
		{"the whole path", Assignment{CustomerName: "Acme", ProjectName: "Migration", Name: "Dev"},
			"Acme / Migration / Dev"},
		{"no customer", Assignment{ProjectName: "Migration", Name: "Dev"}, "Migration / Dev"},
		{"nothing joined", Assignment{Name: "Dev"}, "Dev"},
	} {
		if got := assignment.assignment.Label(); got != assignment.want {
			t.Errorf("%s: label = %q, want %q", assignment.name, got, assignment.want)
		}
	}
}

// TestValidationRefusesWhatStorageWouldAccept.
//
// The database would happily store every one of these. Each is a record that
// looks fine in a table and is wrong on a screen: a nameless customer, a project
// belonging to nobody, a negative rate.
func TestValidationRefusesWhatStorageWouldAccept(t *testing.T) {
	if err := (Customer{Name: "Acme", Currency: "SEK"}).Validate(); err != nil {
		t.Errorf("a valid customer was refused: %v", err)
	}
	for _, customer := range []Customer{
		{Name: ""},
		{Name: "   "},
		{Name: strings.Repeat("x", 201)},
		{Name: "Acme", Currency: "SEKK"},
	} {
		if err := customer.Validate(); err == nil {
			t.Errorf("an invalid customer was accepted: %+v", customer)
		}
	}

	if err := (Project{CustomerID: 1, Name: "Migration"}).Validate(); err != nil {
		t.Errorf("a valid project was refused: %v", err)
	}
	for _, project := range []Project{
		{CustomerID: 1, Name: ""},
		{Name: "Migration"},
		{CustomerID: 1, Name: "Migration", RateMinor: -1},
	} {
		if err := project.Validate(); err == nil {
			t.Errorf("an invalid project was accepted: %+v", project)
		}
	}

	if err := (Assignment{ProjectID: 1, Name: "Dev"}).Validate(); err != nil {
		t.Errorf("a valid assignment was refused: %v", err)
	}
	for _, assignment := range []Assignment{
		{ProjectID: 1, Name: " "},
		{Name: "Dev"},
		{ProjectID: 1, Name: "Dev", RateMinor: -100},
	} {
		if err := assignment.Validate(); err == nil {
			t.Errorf("an invalid assignment was accepted: %+v", assignment)
		}
	}
}

// TestArchivingIsNotDeleting.
//
// Nothing in this application deletes a customer, a project or an assignment,
// because the invoiced history hangs off them. Archived is the retirement, and
// the predicate is what every picker filters on.
func TestArchivingIsNotDeleting(t *testing.T) {
	at := time.Now()
	if (Customer{}).Archived() || (Project{}).Archived() || (Assignment{}).Archived() {
		t.Error("a record with no archive date reports itself archived")
	}
	if !(Customer{ArchivedAt: &at}).Archived() ||
		!(Project{ArchivedAt: &at}).Archived() ||
		!(Assignment{ArchivedAt: &at}).Archived() {
		t.Error("an archived record does not report itself archived")
	}
}

// TestOnlyStaffSeeMoney.
//
// The presentation convenience beside auth.ActionViewMoney. It is not the
// enforcement - the authoriser is - but it decides whether a column is rendered
// at all, and a wrong answer here shows a client their customer's rate.
func TestOnlyStaffSeeMoney(t *testing.T) {
	for role, want := range map[Role]bool{
		RoleAdmin:             true,
		RoleManager:           true,
		RoleMember:            true,
		RoleClient:            false,
		Role("something-new"): false,
		Role(""):              false,
	} {
		if got := (User{Role: role}).CanSeeMoney(); got != want {
			t.Errorf("%q may see money = %v, want %v", role, got, want)
		}
	}
}

// TestAnExpenseIsBilledAtItsMarkup.
//
// And a non-billable one is billed at nothing, rather than carrying a figure
// that a later change to the billable flag would start invoicing.
func TestAnExpenseIsBilledAtItsMarkup(t *testing.T) {
	for _, expense := range []struct {
		name    string
		expense Expense
		want    int64
	}{
		{"no markup", Expense{AmountMinor: 10000, Currency: "SEK", Billable: true}, 10000},
		{"ten percent", Expense{AmountMinor: 10000, Currency: "SEK", Billable: true, MarkupPercent: 10}, 11000},
		{"not billable", Expense{AmountMinor: 10000, Currency: "SEK", MarkupPercent: 10}, 0},
		{"rounds to the minor unit", Expense{AmountMinor: 333, Currency: "SEK", Billable: true, MarkupPercent: 15}, 383},
	} {
		e := expense.expense
		e.ApplyMarkup()
		if e.BilledMinor != expense.want {
			t.Errorf("%s: billed %d, want %d", expense.name, e.BilledMinor, expense.want)
		}
	}

	valid := Expense{ProjectID: 1, UserID: 1, SpentOn: "2026-03-16", AmountMinor: 100}
	if err := valid.Validate(); err != nil {
		t.Errorf("a valid expense was refused: %v", err)
	}
	for _, invalid := range []Expense{
		{UserID: 1, SpentOn: "2026-03-16", AmountMinor: 100},
		{ProjectID: 1, SpentOn: "2026-03-16", AmountMinor: 100},
		{ProjectID: 1, UserID: 1, SpentOn: "16/03/2026", AmountMinor: 100},
		{ProjectID: 1, UserID: 1, SpentOn: "2026-03-16", AmountMinor: 0},
		{ProjectID: 1, UserID: 1, SpentOn: "2026-03-16", AmountMinor: -100},
		{ProjectID: 1, UserID: 1, SpentOn: "2026-03-16", AmountMinor: 100, MarkupPercent: -1},
		{ProjectID: 1, UserID: 1, SpentOn: "2026-03-16", AmountMinor: 100, MarkupPercent: 1001},
		{ProjectID: 1, UserID: 1, SpentOn: "2026-03-16", AmountMinor: 100,
			Description: strings.Repeat("x", 2001)},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("an invalid expense was accepted: %+v", invalid)
		}
	}
}

// TestEveryDisplaySettingDegradesToSomethingTheInterfaceCanDraw.
//
// A value nobody recognises reaches these from an old database, a hand-edited
// row or a future version's downgrade. The rule for all four is the same: never
// return the unrecognised value, and never return nothing - a page whose
// navigation has no layout at all is unusable.
func TestEveryDisplaySettingDegradesToSomethingTheInterfaceCanDraw(t *testing.T) {
	unknown := "no-such-value"

	for _, setting := range []struct {
		name      string
		valid     func(string) bool
		orDefault func(string) string
		key       func(string) string
		prefix    string
	}{
		{"nav position",
			func(s string) bool { return NavPosition(s).Valid() },
			func(s string) string { return string(NavPosition(s).OrDefault()) },
			func(s string) string { return NavPosition(s).MessageKey() },
			"settings.nav."},
		{"clock format",
			func(s string) bool { return ClockFormat(s).Valid() },
			func(s string) string { return string(ClockFormat(s).OrDefault()) },
			func(s string) string { return ClockFormat(s).MessageKey() },
			"settings.clock."},
		{"date format",
			func(s string) bool { return DateFormat(s).Valid() },
			func(s string) string { return string(DateFormat(s).OrDefault()) },
			func(s string) string { return DateFormat(s).MessageKey() },
			"settings.date."},
		{"day overflow",
			func(s string) bool { return DayOverflow(s).Valid() },
			func(s string) string { return string(DayOverflow(s).OrDefault()) },
			func(s string) string { return DayOverflow(s).MessageKey() },
			"settings.overflow."},
	} {
		if setting.valid(unknown) {
			t.Errorf("%s: an unrecognised value reported itself valid", setting.name)
		}
		fallback := setting.orDefault(unknown)
		if fallback == "" || fallback == unknown {
			t.Errorf("%s: an unrecognised value degraded to %q", setting.name, fallback)
		}
		if !setting.valid(fallback) {
			t.Errorf("%s: the fallback %q is not itself valid", setting.name, fallback)
		}
		if !setting.valid(setting.orDefault("")) {
			t.Errorf("%s: an empty value degraded to something invalid", setting.name)
		}
		// The message key has to name the fallback rather than the unrecognised
		// value, or the page renders a missing-key marker where a label belongs.
		if key := setting.key(unknown); key != setting.prefix+fallback {
			t.Errorf("%s: an unrecognised value produced the key %q", setting.name, key)
		}
	}
}

// TestTheDayWindowIsAlwaysDrawable.
//
// The pane is drawn from these two numbers, and a start after its end, or a
// range of zero hours, is a division by zero on somebody's screen.
func TestTheDayWindowIsAlwaysDrawable(t *testing.T) {
	for _, window := range [][2]int{
		{8, 18},
		{18, 8},  // backwards, which a form makes easy to type
		{9, 9},   // no hours at all
		{-4, 40}, // outside the day in both directions
		{0, 0},
		{0, 24},
	} {
		normalised := NormaliseDayWindow(window[0], window[1])
		if normalised.StartHour < 0 || normalised.EndHour > 24 {
			t.Errorf("%v normalised outside the day: %+v", window, normalised)
		}
		if normalised.EndHour <= normalised.StartHour {
			t.Errorf("%v normalised to a window of no hours: %+v", window, normalised)
		}
	}

	// A window that is already sensible is left alone rather than replaced by
	// the default: somebody who set nine to five means nine to five.
	if got := NormaliseDayWindow(9, 17); got != (DayWindow{StartHour: 9, EndHour: 17}) {
		t.Errorf("a valid window was rewritten to %+v", got)
	}
}

// TestNearestRoundsShortEntriesToNothing.
//
// The asymmetry the property above had to make an exception for, on its own so
// that it is visible rather than buried in a loop.
//
// Under a nearest-quarter-hour rule a five-minute entry bills nothing. The
// entry is still on the timesheet with its five minutes; it is the billable
// figure that goes to zero, and the invoice line simply is not there. "Down"
// treats the same case as work that must not disappear and bills the increment.
//
// Both are defensible - nearest is doing what nearest means - and they cannot
// both be the intended rule for the same argument. Recorded here so that
// whoever decides has the case in front of them.
func TestNearestRoundsShortEntriesToNothing(t *testing.T) {
	quarterHour := int64(900)

	nearest := RoundingRule{Mode: RoundNearest, IncrementSeconds: quarterHour}
	if got := nearest.Apply(300); got != 0 {
		t.Errorf("nearest/900 billed a five-minute entry as %d; the behaviour has "+
			"changed, and the argument in TestRoundingNeverMovesAValueFurtherThanTheIncrement "+
			"needs revisiting", got)
	}

	down := RoundingRule{Mode: RoundDown, IncrementSeconds: quarterHour}
	if got := down.Apply(300); got != quarterHour {
		t.Errorf("down/900 billed a five-minute entry as %d, want the increment", got)
	}

	// A minimum is the setting that answers this today: with one, no entry that
	// happened bills nothing under any mode.
	withMinimum := RoundingRule{Mode: RoundNearest, IncrementSeconds: quarterHour, MinimumSeconds: quarterHour}
	if got := withMinimum.Apply(300); got != quarterHour {
		t.Errorf("a minimum did not rescue a five-minute entry: %d", got)
	}
}
