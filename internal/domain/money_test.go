package domain

import (
	"errors"
	"testing"
)

// TestMoneyAdd covers the zero-value currency adoption that makes summing from an
// empty accumulator work, and the refusal to mix currencies (ASR-014).
func TestMoneyAdd(t *testing.T) {
	tests := []struct {
		name      string
		a, b      Money
		wantMinor int64
		wantCur   string
		wantErr   bool
	}{
		{"same currency", NewMoney(1000, "SEK"), NewMoney(250, "SEK"), 1250, "SEK", false},
		{"zero adopts currency", Money{}, NewMoney(250, "EUR"), 250, "EUR", false},
		{"adding zero keeps currency", NewMoney(250, "EUR"), Money{}, 250, "EUR", false},
		{"negative", NewMoney(1000, "SEK"), NewMoney(-1500, "SEK"), -500, "SEK", false},
		{"mismatch is refused", NewMoney(1000, "SEK"), NewMoney(1, "EUR"), 0, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.a.Add(tc.b)
			if tc.wantErr {
				if !errors.Is(err, ErrCurrencyMismatch) {
					t.Fatalf("want ErrCurrencyMismatch, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Minor != tc.wantMinor || got.Currency != tc.wantCur {
				t.Errorf("got %d %q, want %d %q", got.Minor, got.Currency, tc.wantMinor, tc.wantCur)
			}
		})
	}
}

// TestMulDurationHours pins the rate arithmetic, including the half-away-from-zero
// boundary that decides invoice cents.
func TestMulDurationHours(t *testing.T) {
	tests := []struct {
		name      string
		rateMinor int64
		seconds   int64
		want      int64
	}{
		{"one hour at 100.00", 10000, 3600, 10000},
		{"half hour at 100.00", 10000, 1800, 5000},
		{"quarter hour at 100.00", 10000, 900, 2500},
		{"zero duration", 10000, 0, 0},
		{"zero rate", 0, 3600, 0},
		// 1 second at 36.00/h is exactly 1 minor unit.
		{"exact single unit", 3600, 1, 1},
		// 1 second at 18.00/h is 0.5 minor units: ties round away from zero.
		{"tie rounds up", 1800, 1, 1},
		// 1 second at 17.99/h is 0.4997: rounds down.
		{"below tie rounds down", 1799, 1, 0},
		{"negative tie rounds away from zero", -1800, 1, -1},
		// A realistic figure: 7h30m at 1250.00/h.
		{"seven and a half hours", 125000, 27000, 937500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewMoney(tc.rateMinor, "SEK").MulDurationHours(tc.seconds)
			if got.Minor != tc.want {
				t.Errorf("got %d, want %d", got.Minor, tc.want)
			}
			if got.Currency != "SEK" {
				t.Errorf("currency lost: %q", got.Currency)
			}
		})
	}
}

// TestMoneyNoDrift is the regression this whole type exists to prevent: summing a
// third of an hour's billing a thousand times must be exact.
func TestMoneyNoDrift(t *testing.T) {
	rate := NewMoney(10000, "EUR") // 100.00/h
	total := Money{}
	for i := 0; i < 1000; i++ {
		amount := rate.MulDurationHours(1200) // 20 minutes = 33.33...
		var err error
		if total, err = total.Add(amount); err != nil {
			t.Fatal(err)
		}
	}
	// 1200s at 100.00/h is 3333.33 minor units, rounded to 3333 per entry.
	if want := int64(3333 * 1000); total.Minor != want {
		t.Errorf("got %d, want %d", total.Minor, want)
	}
}

func TestParseMoney(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1234.50", 123450, false},
		{"1234,50", 123450, false},
		{"1 234.50", 123450, false},
		{"1234", 123400, false},
		{"0", 0, false},
		{"", 0, false},
		{"-50.25", -5025, false},
		{".5", 50, false},
		{"1.5", 150, false},
		{"1.005", 100, false}, // sub-minor-unit precision is truncated
		{"abc", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseMoney(tc.in, "sek")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Minor != tc.want {
				t.Errorf("got %d, want %d", got.Minor, tc.want)
			}
			if got.Currency != "SEK" {
				t.Errorf("currency not upper-cased: %q", got.Currency)
			}
		})
	}
}

func TestMoneyString(t *testing.T) {
	tests := []struct {
		money Money
		want  string
	}{
		{NewMoney(123450, "SEK"), "1234.50 SEK"},
		{NewMoney(5, "EUR"), "0.05 EUR"},
		{NewMoney(-5025, "USD"), "-50.25 USD"},
		{NewMoney(0, "SEK"), "0.00 SEK"},
	}
	for _, tc := range tests {
		if got := tc.money.String(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
