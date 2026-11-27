package domain

import (
	"testing"
	"time"
)

// A run's two derived answers.
//
// Days is the one worth testing: a period stated as two dates is read
// inclusively by the person who typed it - "March" means the 1st to the 31st,
// which is thirty-one days - and an off-by-one here would put the wrong period
// on an invoice.
func TestABillingRunCountsItsDaysInclusively(t *testing.T) {
	for _, c := range []struct {
		name     string
		from, to string
		want     int
	}{
		{"a whole month", "2026-03-01", "2026-03-31", 31},
		{"a single day", "2026-03-16", "2026-03-16", 1},
		{"a fortnight", "2026-03-02", "2026-03-15", 14},
		{"across a month boundary", "2026-02-24", "2026-03-02", 7},
		// Nonsense in, nothing out. A run whose dates do not read as dates has
		// no length rather than a negative one.
		{"backwards", "2026-03-31", "2026-03-01", 0},
		{"not a date", "March", "2026-03-31", 0},
		{"no end", "2026-03-01", "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := BillingRun{StartsOn: c.from, EndsOn: c.to}
			if got := run.Days(); got != c.want {
				t.Errorf("%s to %s is %d days, want %d", c.from, c.to, got, c.want)
			}
		})
	}
}

func TestARunIsVoidedOnlyOnceItHasBeen(t *testing.T) {
	var run BillingRun
	if run.Voided() {
		t.Error("a run with no voided time reads as voided")
	}
	run.VoidedAt = time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	if !run.Voided() {
		t.Error("a run with a voided time does not read as voided")
	}
}
