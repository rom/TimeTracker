package service

import (
	"context"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// What a download will contain, before it is downloaded.
//
// An export is the basis of an invoice. The screen it is taken from shows fifty
// rows at a time and the download covers the whole filter, so the two never
// looked alike - and the figure that ends up on an invoice was first seen in a
// spreadsheet, after the download, where it is too late for "that is not the
// period I meant".
//
// This answers the questions somebody has before downloading: how much is in
// it, what it comes to, who it is for, and what is deliberately not in it. It
// reads the same filter the export reads, through the same functions, so it
// cannot describe a different document from the one that arrives.

// ExportPreview summarises what an export of a filter would contain.
type ExportPreview struct {
	// From and To bound the period, as the export states it. To is exclusive.
	From, To time.Time
	// Location is the zone the dates are expressed in.
	Location *time.Location

	// Entries is how many rows the download will hold - over the whole filter,
	// not the page the screen happens to be showing.
	Entries int
	// Totals are the figures at the foot of the document.
	Totals domain.Totals
	// AmountsByCurrency is what it comes to. There is no conversion anywhere in
	// this application, so a mixed-currency selection shows several totals
	// rather than one meaningless number.
	AmountsByCurrency map[string]int64

	// ByCustomer is the summary an invoice is written from: one block per
	// customer, each with its projects.
	ByCustomer []BillingGroup

	// Excluded is what the filter matched and the document will leave out, with
	// the reason. A total that silently omits a flagged entry is a figure
	// nobody can reconcile against the screen they are looking at.
	Excluded []ExcludedRows

	// Truncated says the preview summarised only the first rows of a very large
	// selection. The counts and totals are still over all of it; only the
	// per-customer breakdown is capped, because a thousand-customer table is
	// not a preview of anything.
	Truncated bool
}

// BillingGroup is one customer's work in the period.
type BillingGroup struct {
	Customer string
	// Currency is what this customer is invoiced in. Amounts within a group
	// are always in one currency, because a customer has one.
	Currency string
	// Seconds recorded and BillableSeconds after the rounding rule, kept apart
	// so a client can see the difference rather than having to trust it.
	Seconds         int64
	BillableSeconds int64
	AmountMinor     int64
	Entries         int
	Projects        []BillingLine
}

// BillingLine is one project within a customer's block.
type BillingLine struct {
	Project         string
	Seconds         int64
	BillableSeconds int64
	AmountMinor     int64
	Entries         int
}

// ExcludedRows is one reason rows are left out, and how many.
type ExcludedRows struct {
	// Reason is a code, so the screen translates it.
	Reason  string
	Count   int
	Seconds int64
}

// The reasons an entry the filter matched does not reach the document.
const (
	// ExcludedFlagged is an entry marked for review. It counts towards nothing
	// until somebody resolves it, here as everywhere else.
	ExcludedFlagged = "flagged"
	// ExcludedPending is a proposal nobody has accepted, which is a claim
	// rather than a record.
	ExcludedPending = "pending"
	// ExcludedRunning is a timer with no end, which has no length to bill.
	ExcludedRunning = "running"
	// ExcludedNotBillable is work recorded as not billable. It is in the
	// document as a line and contributes nothing to the amount, which is worth
	// saying when somebody is checking a total against a row count.
	ExcludedNotBillable = "not_billable"
)

// maxPreviewGroups caps the per-customer breakdown.
//
// A preview of two hundred customers is a report in its own right, and the
// person looking at one is trying to decide whether to press download. The
// counts and the totals are still over everything.
const maxPreviewGroups = 50

// PreviewExport summarises what exporting a filter would produce.
//
// It deliberately takes the same filter the export takes, and clears the paging
// the same way the export clears it: a preview of the page would describe a
// document nobody is going to get.
func (s *Service) PreviewExport(ctx context.Context, filter EntryFilter) (ExportPreview, error) {
	// An export covers the filter, never the page.
	filter.Limit, filter.Offset = 0, 0

	entries, err := s.Entries(ctx, filter)
	if err != nil {
		return ExportPreview{}, err
	}

	actor, err := auth.MustUser(ctx)
	if err != nil {
		return ExportPreview{}, err
	}
	loc := locationFor(actor)

	preview := ExportPreview{
		From: filter.From, To: filter.To, Location: loc,
		Entries:           len(entries),
		AmountsByCurrency: map[string]int64{},
	}

	// The same totals function every other screen uses, so the foot of this
	// screen and the foot of the document are the same arithmetic.
	preview.Totals = s.totals(entries)

	excluded := map[string]*ExcludedRows{}
	note := func(reason string, seconds int64) {
		row, ok := excluded[reason]
		if !ok {
			row = &ExcludedRows{Reason: reason}
			excluded[reason] = row
		}
		row.Count++
		row.Seconds += seconds
	}

	groups := map[string]*BillingGroup{}
	lines := map[string]map[string]*BillingLine{}

	for _, entry := range entries {
		switch {
		case entry.Running():
			note(ExcludedRunning, 0)
		case entry.Status == domain.StatusPending:
			note(ExcludedPending, entry.DurationSeconds)
		case entry.Flagged:
			note(ExcludedFlagged, entry.DurationSeconds)
		case !entry.Billable:
			note(ExcludedNotBillable, entry.DurationSeconds)
		}

		// Only what counts reaches a figure, which is the same rule every total
		// in this application uses.
		if !entry.Counts() {
			continue
		}

		preview.AmountsByCurrency[entry.Currency] += entry.AmountMinor

		group, ok := groups[entry.CustomerName]
		if !ok {
			group = &BillingGroup{Customer: entry.CustomerName}
			groups[entry.CustomerName] = group
			lines[entry.CustomerName] = map[string]*BillingLine{}
		}
		// The first entry that actually carries one. A non-billable entry has
		// no rate and therefore no currency, so taking it from whichever entry
		// came first would leave a customer's amount rendered as a bare number
		// whenever their most recent entry happened to be unbillable.
		if group.Currency == "" {
			group.Currency = entry.Currency
		}
		group.Seconds += entry.DurationSeconds
		group.BillableSeconds += entry.BillableSeconds
		group.AmountMinor += entry.AmountMinor
		group.Entries++

		line, ok := lines[entry.CustomerName][entry.ProjectName]
		if !ok {
			line = &BillingLine{Project: entry.ProjectName}
			lines[entry.CustomerName][entry.ProjectName] = line
		}
		line.Seconds += entry.DurationSeconds
		line.BillableSeconds += entry.BillableSeconds
		line.AmountMinor += entry.AmountMinor
		line.Entries++
	}

	// An empty currency key means entries with no amount on them at all, which
	// is not a currency and should not appear as a total.
	delete(preview.AmountsByCurrency, "")

	for customer, group := range groups {
		for _, line := range lines[customer] {
			group.Projects = append(group.Projects, *line)
		}
		sort.Slice(group.Projects, func(i, j int) bool {
			return group.Projects[i].Project < group.Projects[j].Project
		})
		preview.ByCustomer = append(preview.ByCustomer, *group)
	}
	// By customer name, so the same selection reads the same way every time.
	sort.Slice(preview.ByCustomer, func(i, j int) bool {
		return preview.ByCustomer[i].Customer < preview.ByCustomer[j].Customer
	})
	if len(preview.ByCustomer) > maxPreviewGroups {
		preview.ByCustomer = preview.ByCustomer[:maxPreviewGroups]
		preview.Truncated = true
	}

	for _, reason := range []string{
		ExcludedFlagged, ExcludedPending, ExcludedRunning, ExcludedNotBillable,
	} {
		if row, ok := excluded[reason]; ok {
			preview.Excluded = append(preview.Excluded, *row)
		}
	}

	return preview, nil
}

// Any reports whether the selection holds anything at all, so a screen can show
// its empty state rather than a table of nothing.
func (p ExportPreview) Any() bool { return p.Entries > 0 }

// ExcludedCount is how many matched rows will not reach a figure.
func (p ExportPreview) ExcludedCount() int {
	var n int
	for _, row := range p.Excluded {
		n += row.Count
	}
	return n
}
