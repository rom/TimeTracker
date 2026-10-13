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

// A currency's minor unit is not always a hundredth.
//
// This application held every currency at two decimal places, and validates a
// currency code as any three letters - so an instance invoicing in yen held
// every amount, every rate and every budget at a hundred times its value, and
// nothing in the application disagreed with itself. The arithmetic was right
// throughout; the unit was wrong.

// TestCurrencyExponents pins the three groups and the default.
func TestCurrencyExponents(t *testing.T) {
	for currency, want := range map[string]int{
		// The ordinary case, which is most of the world.
		"SEK": 2, "EUR": 2, "USD": 2, "GBP": 2, "NOK": 2,
		// No subdivision at all.
		"JPY": 0, "KRW": 0, "ISK": 0, "CLP": 0, "VND": 0, "XOF": 0,
		// Thousandths.
		"KWD": 3, "BHD": 3, "OMR": 3, "TND": 3,
		// And the two units of account that use four.
		"CLF": 4, "UYW": 4,
		// Lower case and padded, as a form or an import might deliver it.
		"jpy": 0, " JPY ": 0,
		// An unlisted code gets two. Being wrong this way is the safe
		// direction: an amount out by a hundred is noticed on the first
		// invoice, and one out by nothing is not noticed at all.
		"ZZZ": 2, "": 2,
	} {
		if got := CurrencyExponent(currency); got != want {
			t.Errorf("CurrencyExponent(%q) = %d, want %d", currency, got, want)
		}
	}
}

// TestAnAmountIsParsedInItsCurrencysUnit.
func TestAnAmountIsParsedInItsCurrencysUnit(t *testing.T) {
	for _, tc := range []struct {
		in       string
		currency string
		want     int64
		why      string
	}{
		{"100", "SEK", 10000, "a hundred kronor is ten thousand öre"},
		{"100", "JPY", 100, "a hundred yen is a hundred yen"},
		{"1234", "ISK", 1234, "and krónur have no aurar either"},
		{"100", "KWD", 100000, "a dinar is a thousand fils"},
		{"1.5", "KWD", 1500, "and a decimal pads to three places, not two"},
		{"1.234", "KWD", 1234, "all three of them"},
		{"1.2345", "KWD", 1234, "and the fourth is more precision than it holds"},
		// A figure typed with decimals in a currency that has none. Truncated
		// rather than refused: the extra digits of a two-decimal currency
		// already are, and a paste from a spreadsheet should not be an error.
		{"100.50", "JPY", 100, "decimals in yen are not a hundredth of anything"},
		{"-250", "JPY", -250, "and the sign survives"},
		{"1 234", "JPY", 1234, "as do the thousands separators"},
	} {
		t.Run(tc.currency+" "+tc.in, func(t *testing.T) {
			got, err := ParseMoney(tc.in, tc.currency)
			if err != nil {
				t.Fatalf("ParseMoney(%q, %q): %v", tc.in, tc.currency, err)
			}
			if got.Minor != tc.want {
				t.Errorf("ParseMoney(%q, %q) = %d minor units, want %d - %s",
					tc.in, tc.currency, got.Minor, tc.want, tc.why)
			}
		})
	}
}

// TestAnAmountIsWrittenInItsCurrencysUnit.
//
// The other half, and the one an invoice shows: "1234.00 JPY" is not a smaller
// amount than "1234 JPY", it is a figure nobody writing in yen would recognise.
func TestAnAmountIsWrittenInItsCurrencysUnit(t *testing.T) {
	for _, tc := range []struct {
		money       Money
		wantDecimal string
		wantString  string
	}{
		{NewMoney(123450, "SEK"), "1234.50", "1234.50 SEK"},
		{NewMoney(1234, "JPY"), "1234", "1234 JPY"},
		{NewMoney(0, "JPY"), "0", "0 JPY"},
		{NewMoney(-1234, "JPY"), "-1234", "-1234 JPY"},
		{NewMoney(1234567, "KWD"), "1234.567", "1234.567 KWD"},
		{NewMoney(7, "KWD"), "0.007", "0.007 KWD"},
		{NewMoney(5, "SEK"), "0.05", "0.05 SEK"},
		// No currency at all is a bare figure, and still two places: the zero
		// value combines with anything, so it has to render as something.
		{NewMoney(150, ""), "1.50", "1.50"},
	} {
		t.Run(tc.wantString, func(t *testing.T) {
			if got := tc.money.Decimal(); got != tc.wantDecimal {
				t.Errorf("Decimal() = %q, want %q", got, tc.wantDecimal)
			}
			if got := tc.money.String(); got != tc.wantString {
				t.Errorf("String() = %q, want %q", got, tc.wantString)
			}
		})
	}
}

// TestWhatIsWrittenCanBeTypedBack.
//
// The round trip, which is what makes a figure on a screen safe to retype into
// a form. It has to hold in every currency, and it is the property the
// two-decimal assumption broke: 1234 yen rendered as "1234.00" and read back as
// 123400.
func TestWhatIsWrittenCanBeTypedBack(t *testing.T) {
	for _, currency := range []string{"SEK", "EUR", "JPY", "ISK", "KWD", "CLF", ""} {
		for _, minor := range []int64{0, 1, 7, 50, 99, 100, 1234, 123450, -1234} {
			original := NewMoney(minor, currency)
			parsed, err := ParseMoney(original.Decimal(), currency)
			if err != nil {
				t.Fatalf("ParseMoney(%q, %q): %v", original.Decimal(), currency, err)
			}
			if parsed.Minor != minor {
				t.Errorf("%d %s rendered as %q and read back as %d",
					minor, currency, original.Decimal(), parsed.Minor)
			}
		}
	}
}
