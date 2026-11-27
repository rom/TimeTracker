package domain

import "time"

// What has been billed.
//
// A BillingRun is this application's record that an invoice went out: which
// customer, which days, and what it came to. It is deliberately not an invoice
// - this application does not issue or number those (DESIGN.md §11) - it is the
// answer to "has this time been charged for", which nothing could give before
// it existed.

// BillingRun is one invoice's worth of time, as recorded here.
type BillingRun struct {
	ID         int64
	CustomerID int64
	// StartsOn and EndsOn are the days covered, both inclusive, as the person
	// billing chose them.
	StartsOn string
	EndsOn   string
	// Reference is the invoice number in whatever system issues invoices, and
	// Note is anything else worth saying about it. Both are free text.
	Reference string
	Note      string
	// Currency, EntryCount, BillableSeconds and AmountMinor are what the run
	// came to when it was made. Copied rather than derived, for the reason an
	// entry's own billing snapshot is copied: an invoice is a statement about a
	// moment, and next quarter's rate must not change what last quarter's
	// invoice said.
	Currency        string
	EntryCount      int
	BillableSeconds int64
	AmountMinor     int64
	CreatedBy       int64
	CreatedAt       time.Time
	// VoidedAt is zero until the run is cancelled. Voiding releases the
	// entries, which become unbilled again.
	VoidedAt time.Time
	VoidedBy int64
	// CustomerName is filled in by listings, for a screen that should not have
	// to look each one up.
	CustomerName string
}

// Voided reports whether this run has been cancelled.
func (r BillingRun) Voided() bool { return !r.VoidedAt.IsZero() }

// Days is how many days the run covers, counting both ends.
func (r BillingRun) Days() int {
	const iso = "2006-01-02"
	from, err := time.Parse(iso, r.StartsOn)
	if err != nil {
		return 0
	}
	to, err := time.Parse(iso, r.EndsOn)
	if err != nil {
		return 0
	}
	if to.Before(from) {
		return 0
	}
	return int(to.Sub(from)/(24*time.Hour)) + 1
}
