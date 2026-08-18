package domain

import (
	"errors"
	"testing"
	"time"
)

// Customer rate rules. These decide what appears on an invoice, so the
// arithmetic is pinned exactly rather than approximately, and every "inherit"
// path is covered - an unset rule silently behaving as zero would bill work at
// nothing, which is the most expensive possible failure here.

func TestRateForKind(t *testing.T) {
	base := NewMoney(100000, "SEK") // 1000.00/h

	cases := []struct {
		name  string
		rules RateRules
		kind  EntryKind
		want  int64
		// billable is false only where the customer does not pay for the kind
		// at all, which is different from a rate of zero.
		billable bool
	}{
		{
			name: "no rules at all: every kind bills at the base rate",
			kind: KindOvertime, want: 100000, billable: true,
		},
		{
			name:  "overtime multiplier",
			rules: RateRules{OvertimeMultiplierPct: 150},
			kind:  KindOvertime, want: 150000, billable: true,
		},
		{
			name: "an absolute overtime rate wins over a multiplier",
			rules: RateRules{
				OvertimeRateMinor: 125000, OvertimeMultiplierPct: 200,
			},
			kind: KindOvertime, want: 125000, billable: true,
		},
		{
			name:  "overtime rules do not touch ordinary work",
			rules: RateRules{OvertimeMultiplierPct: 200},
			kind:  KindWork, want: 100000, billable: true,
		},
		{
			name:  "travel bills as work unless told otherwise",
			rules: RateRules{TravelMultiplierPct: 50},
			kind:  KindTravel, want: 100000, billable: true,
		},
		{
			// The multiplier alone is not enough: the customer has to have said
			// travel is billed at its own rate. Otherwise a half-filled form
			// would quietly halve every travel hour.
			name:  "travel at its own rate, by multiplier",
			rules: RateRules{TravelBilling: TravelAtRate, TravelMultiplierPct: 50},
			kind:  KindTravel, want: 50000, billable: true,
		},
		{
			name: "an absolute travel rate wins over a multiplier",
			rules: RateRules{
				TravelBilling: TravelAtRate, TravelRateMinor: 40000, TravelMultiplierPct: 50,
			},
			kind: KindTravel, want: 40000, billable: true,
		},
		{
			name:  "travel at its own rate with nothing set falls back to the base",
			rules: RateRules{TravelBilling: TravelAtRate},
			kind:  KindTravel, want: 100000, billable: true,
		},
		{
			name:  "unbilled travel carries no amount and is not billable",
			rules: RateRules{TravelBilling: TravelUnbilled},
			kind:  KindTravel, want: 0, billable: false,
		},
		{
			name:  "unbilled travel does not stop work being billed",
			rules: RateRules{TravelBilling: TravelUnbilled},
			kind:  KindWork, want: 100000, billable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, billable := tc.rules.RateForKind(tc.kind, base)
			if rate.Minor != tc.want {
				t.Errorf("rate = %d, want %d", rate.Minor, tc.want)
			}
			if billable != tc.billable {
				t.Errorf("billable = %v, want %v", billable, tc.billable)
			}
			if rate.Currency != base.Currency {
				t.Errorf("currency = %q, want %q", rate.Currency, base.Currency)
			}
		})
	}
}

// TestScalePercentIsNotApplyPercent: the two are one character apart at a call
// site and mean very different things. 150% overtime is one and a half times
// the base; a 150% markup is two and a half times the cost.
func TestScalePercentIsNotApplyPercent(t *testing.T) {
	amount := NewMoney(10000, "SEK")

	if got := amount.ScalePercent(150).Minor; got != 15000 {
		t.Errorf("ScalePercent(150) = %d, want 15000", got)
	}
	if got := amount.ApplyPercent(150).Minor; got != 25000 {
		t.Errorf("ApplyPercent(150) = %d, want 25000", got)
	}
	// Rounding is half away from zero at the last minor unit, as everywhere else.
	if got := NewMoney(101, "SEK").ScalePercent(50).Minor; got != 51 {
		t.Errorf("ScalePercent rounds the wrong way: %d, want 51", got)
	}
}

func TestQuantityAmount(t *testing.T) {
	cases := []struct {
		name          string
		quantityMilli int64
		rateMinor     int64
		want          int64
	}{
		{"42.5 km at 25.00", 42500, 2500, 106250},
		{"a whole number of days", 3000, 26000, 78000},
		{"a third of a unit rounds half away from zero", 333, 100, 33},
		{"exactly half a minor unit rounds up", 500, 1, 1},
		{"no quantity is no amount", 0, 2500, 0},
		{"no rate is no amount", 42500, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QuantityAmount(tc.quantityMilli, tc.rateMinor, "SEK")
			if got.Minor != tc.want {
				t.Errorf("QuantityAmount = %d, want %d", got.Minor, tc.want)
			}
		})
	}
}

func TestParseAndFormatQuantity(t *testing.T) {
	cases := []struct {
		text string
		want int64
	}{
		{"42.5", 42500},
		{"42,5", 42500},   // a Swedish keyboard
		{" 42.5 ", 42500}, // pasted from a spreadsheet
		{"3", 3000},
		{".5", 500},
		{"", 0},
		{"1.2345", 1234}, // truncated, not rounded: four decimals is a typo
	}
	for _, tc := range cases {
		got, err := ParseQuantityMilli(tc.text)
		if err != nil {
			t.Errorf("ParseQuantityMilli(%q): %v", tc.text, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseQuantityMilli(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}

	for _, bad := range []string{"-5", "many", "1.2.3"} {
		if _, err := ParseQuantityMilli(bad); err == nil {
			t.Errorf("ParseQuantityMilli(%q) was accepted", bad)
		}
	}

	// What is displayed can be typed back, which is the whole point of having
	// a formatter beside the parser.
	for _, milli := range []int64{42500, 3000, 500, 1} {
		text := FormatQuantity(milli)
		back, err := ParseQuantityMilli(text)
		if err != nil {
			t.Fatalf("FormatQuantity(%d) = %q, which does not parse back: %v", milli, text, err)
		}
		if back != milli {
			t.Errorf("%d formatted as %q parsed back as %d", milli, text, back)
		}
	}
	if got := FormatQuantity(0); got != "" {
		t.Errorf("FormatQuantity(0) = %q, want empty", got)
	}
}

func TestRateRulesValidate(t *testing.T) {
	if err := (RateRules{}).Validate(); err != nil {
		t.Errorf("empty rules were rejected: %v", err)
	}

	bad := []struct {
		name  string
		rules RateRules
	}{
		// A decimal point in the wrong place: 15000% rather than 150%.
		{"an implausible multiplier", RateRules{OvertimeMultiplierPct: 15000}},
		{"a negative rate", RateRules{OvertimeRateMinor: -1}},
		{"a daily threshold beyond a day", RateRules{OvertimeDailyThresholdSeconds: 25 * 3600}},
		{"a weekly threshold beyond a week", RateRules{OvertimeWeeklyThresholdSeconds: 200 * 3600}},
		{"an unknown travel rule", RateRules{TravelBilling: "sometimes"}},
		{"an unknown expense rule", RateRules{ExpenseBilling: "maybe"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rules.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("error = %v, want a validation failure", err)
			}
		})
	}
}

func TestRateRulesConfigured(t *testing.T) {
	if (RateRules{}).Configured() {
		t.Error("empty rules reported as configured")
	}
	if !(RateRules{OvertimeMultiplierPct: 150}).Configured() {
		t.Error("a set rule was not reported as configured")
	}
	// Travel billed at 0% is a real rule and must register as one, which is
	// exactly why "unbilled" is its own state rather than a zero multiplier.
	if !(RateRules{TravelBilling: TravelUnbilled}).Configured() {
		t.Error("unbilled travel was not reported as configured")
	}
}

func TestEntryKindDefaults(t *testing.T) {
	// Every row stored before kinds existed has an empty column, and every
	// reader has to agree that it means work.
	if got := (TimeEntry{}).KindOrDefault(); got != KindWork {
		t.Errorf("an entry with no kind is %q, want work", got)
	}
	valid := TimeEntry{
		AssignmentID: 1, UserID: 1,
		StartedAt: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("the baseline entry is not valid: %v", err)
	}
	unknown := valid
	unknown.Kind = "guesswork"
	if err := unknown.Validate(); err == nil {
		t.Error("an unknown kind was accepted")
	}
	for _, kind := range EntryKinds() {
		known := valid
		known.Kind = kind
		if err := known.Validate(); err != nil {
			t.Errorf("kind %q was rejected: %v", kind, err)
		}
	}
	for _, kind := range EntryKinds() {
		if !kind.Valid() {
			t.Errorf("%q is offered but not valid", kind)
		}
	}
	if EntryKind("").Valid() {
		t.Error("the empty kind should not be valid; it is handled by KindOrDefault")
	}
}

func TestUnitRateFor(t *testing.T) {
	rules := RateRules{MileageRateMinor: 2500, PerDiemMinor: 26000}

	if rate, ok := rules.UnitRateFor(UnitKilometre); !ok || rate != 2500 {
		t.Errorf("mileage = %d, %v", rate, ok)
	}
	if rate, ok := rules.UnitRateFor(UnitDay); !ok || rate != 26000 {
		t.Errorf("per diem = %d, %v", rate, ok)
	}
	if _, ok := rules.UnitRateFor(UnitNone); ok {
		t.Error("an ordinary expense reported a unit rate")
	}
	if _, ok := (RateRules{}).UnitRateFor(UnitKilometre); ok {
		t.Error("a customer with no mileage rule reported one")
	}
}

// Dated terms, and the merge of a project's over its customer's.
//
// Two rules decide every figure this feature produces: the latest revision on
// or before the day wins, and a project's fields lie over its customer's. Both
// are pinned here because getting either wrong changes an invoice quietly.

func TestResolveTermsPicksTheRevisionInForce(t *testing.T) {
	// Newest first, which is the order the store returns them in.
	customer := []ContractTerms{
		{EffectiveFrom: "2026-07-01", Rules: RateRules{OvertimeMultiplierPct: 200}},
		{EffectiveFrom: "2026-01-01", Rules: RateRules{OvertimeMultiplierPct: 150}},
		{EffectiveFrom: "", Rules: RateRules{OvertimeMultiplierPct: 125}},
	}

	cases := []struct {
		day  string
		want int64
	}{
		{"2025-12-31", 125}, // before any dated revision: the open-ended one
		{"2026-01-01", 150}, // the day a revision starts, it applies
		{"2026-06-30", 150},
		{"2026-07-01", 200},
		{"2027-01-01", 200}, // the latest keeps applying until superseded
	}
	for _, tc := range cases {
		got := ResolveTerms(customer, nil, tc.day).OvertimeMultiplierPct
		if got != tc.want {
			t.Errorf("on %s: overtime = %d%%, want %d%%", tc.day, got, tc.want)
		}
	}
}

func TestResolveTermsWithNoRevisionYet(t *testing.T) {
	// Terms agreed for a future date do not apply before it, and with nothing
	// else in force the answer is "no terms" rather than the future ones.
	future := []ContractTerms{
		{EffectiveFrom: "2027-01-01", Rules: RateRules{OvertimeMultiplierPct: 200}},
	}
	if got := ResolveTerms(future, nil, "2026-08-18"); got.Configured() {
		t.Errorf("terms that start next year applied today: %+v", got)
	}
	if got := ResolveTerms(nil, nil, "2026-08-18"); got.Configured() {
		t.Errorf("no terms at all resolved to %+v", got)
	}
}

// TestProjectTermsMergeOverTheCustomers is the per-project requirement: a
// project that differs only in overtime says only that, and everything else
// keeps following the account.
func TestProjectTermsMergeOverTheCustomers(t *testing.T) {
	customer := []ContractTerms{{Rules: RateRules{
		OvertimeMultiplierPct: 150,
		TravelBilling:         TravelUnbilled,
		MileageRateMinor:      2500,
		ExpenseMarkupPct:      10,
	}}}
	project := []ContractTerms{{Rules: RateRules{
		OvertimeMultiplierPct: 200,
	}}}

	got := ResolveTerms(customer, project, "2026-08-18")

	if got.OvertimeMultiplierPct != 200 {
		t.Errorf("overtime = %d, want the project's 200", got.OvertimeMultiplierPct)
	}
	// Everything the project said nothing about still follows the account.
	if got.TravelBilling != TravelUnbilled {
		t.Errorf("travel = %q, want the customer's unbilled", got.TravelBilling)
	}
	if got.MileageRateMinor != 2500 {
		t.Errorf("mileage = %d, want the customer's", got.MileageRateMinor)
	}
	if got.ExpenseMarkupPct != 10 {
		t.Errorf("markup = %d, want the customer's", got.ExpenseMarkupPct)
	}
}

// TestProjectCanOverrideTravelBackToWork is why the enumerations needed an
// explicit value for their default. Without one, "travel is billed as work
// here" and "say nothing about travel" would be the same string, and a project
// could never override a customer that does not pay for travel.
func TestProjectCanOverrideTravelBackToWork(t *testing.T) {
	customer := []ContractTerms{{Rules: RateRules{TravelBilling: TravelUnbilled}}}

	silent := ResolveTerms(customer, []ContractTerms{{Rules: RateRules{OvertimeMultiplierPct: 150}}}, "2026-08-18")
	if silent.TravelBilling != TravelUnbilled {
		t.Errorf("a project saying nothing changed travel to %q", silent.TravelBilling)
	}

	explicit := ResolveTerms(customer, []ContractTerms{{Rules: RateRules{TravelBilling: TravelAsWork}}}, "2026-08-18")
	if explicit.TravelBilling != TravelAsWork {
		t.Errorf("a project saying travel is work resolved to %q", explicit.TravelBilling)
	}
	// And that actually changes the money.
	base := NewMoney(100000, "SEK")
	if _, billable := explicit.RateForKind(KindTravel, base); !billable {
		t.Error("travel is still unbilled after the project overrode it")
	}
}

// TestMergeDoesNotMutateTheBase: the customer's terms are resolved once and may
// be laid under several projects.
func TestMergeDoesNotMutateTheBase(t *testing.T) {
	base := RateRules{OvertimeMultiplierPct: 150, MileageRateMinor: 2500}
	overlay := RateRules{OvertimeMultiplierPct: 200}

	merged := overlay.Merge(base)
	if base.OvertimeMultiplierPct != 150 {
		t.Errorf("the base was modified: %d", base.OvertimeMultiplierPct)
	}
	if merged.OvertimeMultiplierPct != 200 || merged.MileageRateMinor != 2500 {
		t.Errorf("the merge is wrong: %+v", merged)
	}
}

func TestContractTermsValidate(t *testing.T) {
	valid := ContractTerms{Scope: TermsForCustomer, ScopeID: 1, EffectiveFrom: "2026-01-01"}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid terms were rejected: %v", err)
	}
	// Empty means "always", which is what carried-over terms carry.
	open := ContractTerms{Scope: TermsForProject, ScopeID: 2}
	if err := open.Validate(); err != nil {
		t.Errorf("open-ended terms were rejected: %v", err)
	}

	for _, bad := range []ContractTerms{
		{Scope: "team", ScopeID: 1},
		{Scope: TermsForCustomer},
		{Scope: TermsForCustomer, ScopeID: 1, EffectiveFrom: "last April"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
