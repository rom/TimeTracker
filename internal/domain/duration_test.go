package domain

import "testing"

// TestRoundingApply walks the boundaries of every mode. These are the numbers that
// end up on invoices, so exactly-on-the-increment and one-second-either-side are
// tested explicitly (ASR-014).
func TestRoundingApply(t *testing.T) {
	quarterUp := RoundingRule{Mode: RoundUp, IncrementSeconds: 900}
	quarterNearest := RoundingRule{Mode: RoundNearest, IncrementSeconds: 900}
	quarterDown := RoundingRule{Mode: RoundDown, IncrementSeconds: 900}
	hourMinimum := RoundingRule{Mode: RoundUp, IncrementSeconds: 900, MinimumSeconds: 3600}

	tests := []struct {
		name string
		rule RoundingRule
		in   int64
		want int64
	}{
		{"none passes through", NoRounding, 1234, 1234},
		{"zero stays zero", quarterUp, 0, 0},
		{"negative clamps to zero", quarterUp, -100, 0},
		{"up: one second becomes an increment", quarterUp, 1, 900},
		{"up: exactly on the increment is unchanged", quarterUp, 900, 900},
		{"up: one second over rolls to the next", quarterUp, 901, 1800},
		{"nearest: below the midpoint rounds down", quarterNearest, 449, 0},
		{"nearest: the midpoint rounds up", quarterNearest, 450, 900},
		{"nearest: just under the top rounds up", quarterNearest, 899, 900},
		{"down: truncates", quarterDown, 1799, 900},
		{"down: never erases work entirely", quarterDown, 60, 900},
		{"minimum applies above the increment", hourMinimum, 60, 3600},
		{"minimum does not shrink a longer entry", hourMinimum, 7200, 7200},
		{"minimum never inflates nothing", hourMinimum, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Apply(tc.in); got != tc.want {
				t.Errorf("Apply(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestRoundingRuleRoundTrip guards the persisted form: an entry stores the rule
// that was applied to it, so String and ParseRoundingRule must agree forever.
func TestRoundingRuleRoundTrip(t *testing.T) {
	rules := []RoundingRule{
		NoRounding,
		{Mode: RoundUp, IncrementSeconds: 900},
		{Mode: RoundNearest, IncrementSeconds: 300, MinimumSeconds: 900},
		{Mode: RoundDown, IncrementSeconds: 3600, MinimumSeconds: 3600},
	}
	for _, rule := range rules {
		got := ParseRoundingRule(rule.String())
		if got.Mode != rule.Mode || got.IncrementSeconds != rule.IncrementSeconds || got.MinimumSeconds != rule.MinimumSeconds {
			t.Errorf("round trip of %q gave %+v, want %+v", rule.String(), got, rule)
		}
	}
	// An unrecognised stored value must degrade to no rounding rather than fail,
	// so a historical entry stays readable.
	if got := ParseRoundingRule("something/odd"); got.Mode != RoundNone {
		t.Errorf("unknown rule should degrade to none, got %+v", got)
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1.5", 5400, false},
		{"1,5", 5400, false},
		{"0.25", 900, false},
		{"30", 1800, false}, // a bare integer means minutes
		{"90", 5400, false},
		{"1h", 3600, false},
		{"1h30", 5400, false},
		{"1h30m", 5400, false},
		{"90m", 5400, false},
		{"45s", 45, false},
		{"1:30", 5400, false},
		{"0:15", 900, false},
		{"2h15m30s", 8130, false},
		{" 1h 30m ", 5400, false},
		{"", 0, true},
		{"abc", 0, true},
		{"1:75", 0, true}, // 75 minutes is not a valid clock component
		{"-1", 0, true},
		{"30x", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDuration(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got %d", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseDuration(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{60, "1m"},
		{3600, "1h 00m"},
		{5400, "1h 30m"},
		{27000, "7h 30m"},
		{-5400, "-1h 30m"},
	}
	for _, tc := range tests {
		if got := FormatDuration(tc.in); got != tc.want {
			t.Errorf("FormatDuration(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatDecimalHours pins the export format: these strings go into CSV files
// that a client's finance department reconciles.
func TestFormatDecimalHours(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{3600, "1.00"},
		{5400, "1.50"},
		{900, "0.25"},
		{0, "0.00"},
		{27000, "7.50"},
		{60, "0.02"}, // one minute, rounded to hundredths
	}
	for _, tc := range tests {
		if got := FormatDecimalHours(tc.in); got != tc.want {
			t.Errorf("FormatDecimalHours(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMinimumRoundingRules covers the "minimum N hours" presets: a common
// consultancy arrangement where a call-out is billed as a whole block however
// short it was, but a longer visit bills its real duration.
func TestMinimumRoundingRules(t *testing.T) {
	tests := []struct {
		name string
		key  string
		in   int64
		want int64
	}{
		// Minimum with no increment.
		{"2h minimum: a short call", "none/0/7200", 900, 7200},
		{"2h minimum: exactly two hours", "none/0/7200", 7200, 7200},
		{"2h minimum: a longer visit bills its real length", "none/0/7200", 9000, 9000},
		{"4h minimum", "none/0/14400", 3600, 14400},
		{"8h minimum: a whole day", "none/0/28800", 3600, 28800},
		{"8h minimum: more than a day is not shrunk", "none/0/28800", 36000, 36000},
		// A minimum must never inflate an absence of work.
		{"2h minimum: nothing stays nothing", "none/0/7200", 0, 0},
		// Minimum combined with an increment.
		{"2h then quarter hours: short", "up/900/7200", 600, 7200},
		// 2h01m rounded up to the quarter hour is 2h15m; the minimum does not
		// then apply because the rounded value already exceeds it.
		{"2h then quarter hours: just over", "up/900/7200", 7260, 8100},
		// 4h10m rounds up to 4h15m.
		{"4h then quarter hours", "up/900/14400", 15000, 15300},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule := ParseRoundingRule(tc.key)
			if got := rule.Apply(tc.in); got != tc.want {
				t.Errorf("%s applied to %d = %d, want %d", tc.key, tc.in, got, tc.want)
			}
		})
	}
}

// TestNamedRoundingRulesRoundTrip: the keys are stored on entries, so a preset
// that does not survive a round trip would silently change an invoiced figure.
func TestNamedRoundingRulesRoundTrip(t *testing.T) {
	for _, preset := range NamedRoundingRules {
		parsed := ParseRoundingRule(preset.Key)
		if got := parsed.String(); got != preset.Key {
			t.Errorf("preset %q round-tripped to %q", preset.Key, got)
		}
		// And every preset must have a distinct, non-empty effect description.
		if preset.MessageKey == "" {
			t.Errorf("preset %q has no message key", preset.Key)
		}
	}
}
