package service

import (
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// What a download will contain, before it is downloaded.
//
// The preview's value is that it describes the document that actually arrives.
// A preview that counted the page, or that totalled what the document excludes,
// would be worse than none: the figure on it would be the one somebody quotes.

// marchFilter is a month-wide filter, the shape an invoice is taken over.
func marchFilter() EntryFilter {
	return EntryFilter{
		From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}
}

// TestThePreviewCoversTheFilterRatherThanThePage.
//
// The screen shows fifty rows at a time and the download covers everything the
// filter matches. A preview that inherited the screen's cap would understate
// the document by however many rows did not fit - which is precisely the defect
// the export itself had before its paging was cleared.
func TestThePreviewCoversTheFilterRatherThanThePage(t *testing.T) {
	f := newFixture(t)

	for day := 0; day < 20; day++ {
		mustCreate(t, f, at(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day), 9, 0), 3600)
	}

	// A filter carrying a page's worth of paging, as the screen would hand it
	// over.
	filter := marchFilter()
	filter.Limit, filter.Offset = 5, 0

	preview, err := f.svc.PreviewExport(f.ctx, filter)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Entries != 20 {
		t.Errorf("the preview says the download holds %d entries, want all 20 - "+
			"a preview that inherited the screen's cap would understate the "+
			"document", preview.Entries)
	}
	if preview.Totals.SummedSeconds != 20*3600 {
		t.Errorf("the preview totals %ds, want the 20 hours the download holds",
			preview.Totals.SummedSeconds)
	}
}

// TestThePreviewAgreesWithTheEntriesItDescribes.
//
// The figures on the preview and the figures the export writes come from the
// same entries. This compares the preview against the same filter read
// directly, so the two cannot drift apart without a failure here.
func TestThePreviewAgreesWithTheEntriesItDescribes(t *testing.T) {
	f := newFixture(t)

	// A second customer, so the grouping has something to group.
	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
		Name: "Globex", Currency: "SEK", RateMinor: 100000,
	})
	if err != nil {
		t.Fatalf("create a customer: %v", err)
	}
	otherProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: other.ID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create a project: %v", err)
	}
	otherAssignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: otherProject.ID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create an assignment: %v", err)
	}

	march := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	mustCreate(t, f, at(march, 9, 0), 4*3600)
	mustCreate(t, f, at(march.AddDate(0, 0, 1), 9, 0), 2*3600)
	end := at(march.AddDate(0, 0, 2), 12, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: otherAssignment.ID, StartedAt: at(march.AddDate(0, 0, 2), 9, 0),
		EndedAt: &end, Billable: true,
	}); err != nil {
		t.Fatalf("record for the second customer: %v", err)
	}

	preview, err := f.svc.PreviewExport(f.ctx, marchFilter())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	entries, err := f.svc.Entries(f.ctx, marchFilter())
	if err != nil {
		t.Fatalf("read the same filter: %v", err)
	}

	if preview.Entries != len(entries) {
		t.Errorf("the preview counts %d entries and the filter matches %d",
			preview.Entries, len(entries))
	}

	// The per-customer blocks add up to the whole, which is what makes the
	// breakdown trustworthy rather than decorative.
	var grouped int64
	var counted int
	for _, group := range preview.ByCustomer {
		var fromProjects int64
		for _, line := range group.Projects {
			fromProjects += line.Seconds
		}
		if fromProjects != group.Seconds {
			t.Errorf("%s's projects add up to %ds and its block says %ds",
				group.Customer, fromProjects, group.Seconds)
		}
		grouped += group.Seconds
		counted += group.Entries
	}
	if grouped != preview.Totals.SummedSeconds {
		t.Errorf("the customer blocks add up to %ds and the total says %ds",
			grouped, preview.Totals.SummedSeconds)
	}
	if counted != preview.Entries {
		t.Errorf("the customer blocks hold %d entries and the preview says %d",
			counted, preview.Entries)
	}

	// Two customers, named and in a stable order.
	if len(preview.ByCustomer) != 2 {
		t.Fatalf("%d customers in the breakdown, want 2", len(preview.ByCustomer))
	}
	if preview.ByCustomer[0].Customer > preview.ByCustomer[1].Customer {
		t.Errorf("the customers are not in a stable order: %q then %q",
			preview.ByCustomer[0].Customer, preview.ByCustomer[1].Customer)
	}
	for _, group := range preview.ByCustomer {
		if group.Currency == "" {
			t.Errorf("%s's block has no currency, so its amount is a bare number",
				group.Customer)
		}
	}
}

// TestThePreviewNamesWhatTheDocumentLeavesOut.
//
// A total that silently omitted a flagged entry is a figure nobody can
// reconcile against the screen it came from. Each exclusion is named and
// counted, and none of them reaches the amount.
func TestThePreviewNamesWhatTheDocumentLeavesOut(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	ctx := f.ctx

	march := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	record := func(t *testing.T, start time.Time, hours int, billable bool) domain.TimeEntry {
		t.Helper()
		end := start.Add(time.Duration(hours) * time.Hour)
		entry, err := f.svc.CreateEntry(ctx, EntryInput{
			AssignmentID: assignment.ID, StartedAt: start, EndedAt: &end,
			Billable: billable,
		})
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		return entry
	}

	// Four hours that count.
	record(t, march.Add(9*time.Hour), 4, true)
	// Two hours recorded as not billable.
	record(t, march.AddDate(0, 0, 1).Add(9*time.Hour), 2, false)
	// A flagged entry.
	flagged := record(t, march.AddDate(0, 0, 2).Add(9*time.Hour), 3, true)
	stored, err := f.db.GetEntry(ctx, flagged.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	stored.Flagged = true
	if err := f.db.UpdateEntry(ctx, stored); err != nil {
		t.Fatalf("flag: %v", err)
	}
	// A proposal for the colleague, which the colleague has not accepted.
	proposalEnd := march.AddDate(0, 0, 3).Add(12 * time.Hour)
	if _, err := f.svc.CreateEntry(ctx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: march.AddDate(0, 0, 3).Add(9 * time.Hour),
		EndedAt: &proposalEnd, OnBehalfOf: colleague.ID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}

	preview, err := f.svc.PreviewExport(ctx, marchFilter())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	reasons := map[string]ExcludedRows{}
	for _, row := range preview.Excluded {
		reasons[row.Reason] = row
	}
	for _, want := range []string{ExcludedFlagged, ExcludedNotBillable} {
		if _, ok := reasons[want]; !ok {
			t.Errorf("the preview does not mention %q; it says %v", want, reasons)
		}
	}
	if row := reasons[ExcludedFlagged]; row.Count != 1 || row.Seconds != 3*3600 {
		t.Errorf("the flagged exclusion says %d entries and %ds, want 1 and 3 hours",
			row.Count, row.Seconds)
	}

	// None of it reaches the figure. The amount is the four counting billable
	// hours and nothing else.
	if preview.Totals.SummedSeconds != 6*3600 {
		t.Errorf("the total is %ds; the four billable hours and the two "+
			"non-billable ones are recorded work, the flagged and proposed ones "+
			"are not", preview.Totals.SummedSeconds)
	}
	var amount int64
	for _, group := range preview.ByCustomer {
		amount += group.AmountMinor
	}
	if amount == 0 {
		t.Error("the preview shows no amount at all for four billable hours")
	}

	// A selection with nothing wrong with it says nothing.
	clean := newFixture(t)
	mustCreate(t, clean, at(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), 9, 0), 4*3600)
	tidy, err := clean.svc.PreviewExport(clean.ctx, marchFilter())
	if err != nil {
		t.Fatalf("preview a clean selection: %v", err)
	}
	if len(tidy.Excluded) != 0 {
		t.Errorf("a selection with nothing wrong with it reported %v", tidy.Excluded)
	}
	if tidy.ExcludedCount() != 0 {
		t.Errorf("ExcludedCount is %d for a clean selection", tidy.ExcludedCount())
	}
}

// TestAMixedCurrencySelectionShowsSeveralTotals.
//
// There is no conversion anywhere in this application. One number over two
// currencies would be arithmetic nobody asked for and nobody could check, so a
// mixed selection shows each separately.
func TestAMixedCurrencySelectionShowsSeveralTotals(t *testing.T) {
	f := newFixture(t)

	euro, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
		Name: "Globex", Currency: "EUR", RateMinor: 10000,
	})
	if err != nil {
		t.Fatalf("create a customer: %v", err)
	}
	euroProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: euro.ID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create a project: %v", err)
	}
	euroAssignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: euroProject.ID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create an assignment: %v", err)
	}

	march := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	mustCreate(t, f, at(march, 9, 0), 4*3600)
	end := at(march.AddDate(0, 0, 1), 13, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: euroAssignment.ID, StartedAt: at(march.AddDate(0, 0, 1), 9, 0),
		EndedAt: &end, Billable: true,
	}); err != nil {
		t.Fatalf("record in euros: %v", err)
	}

	preview, err := f.svc.PreviewExport(f.ctx, marchFilter())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	if len(preview.AmountsByCurrency) != 2 {
		t.Errorf("a selection spanning two currencies reports %d totals (%v), "+
			"want one each - there is no conversion anywhere here",
			len(preview.AmountsByCurrency), preview.AmountsByCurrency)
	}
	for currency, amount := range preview.AmountsByCurrency {
		if currency == "" {
			t.Error("an amount is totalled under no currency at all")
		}
		if amount <= 0 {
			t.Errorf("the %s total is %d", currency, amount)
		}
	}
}

// TestAPreviewOfNothing.
//
// A filter matching nothing is a question somebody asked, not an error. The
// screen says so rather than offering a download of an empty document.
func TestAPreviewOfNothing(t *testing.T) {
	f := newFixture(t)

	preview, err := f.svc.PreviewExport(f.ctx, marchFilter())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Any() {
		t.Error("a filter matching nothing reports that it holds something")
	}
	if preview.Entries != 0 || len(preview.ByCustomer) != 0 {
		t.Errorf("an empty selection reports %d entries and %d customers",
			preview.Entries, len(preview.ByCustomer))
	}
	if len(preview.AmountsByCurrency) != 0 {
		t.Errorf("an empty selection reports amounts: %v", preview.AmountsByCurrency)
	}
}

// TestACustomersAmountAlwaysCarriesItsCurrency.
//
// A non-billable entry has no rate and therefore no currency. Taking a
// customer's currency from whichever of their entries came first left it empty
// whenever that entry happened to be unbillable - and the amount then rendered
// as a bare number, which is not an amount of money.
//
// Found on screen rather than by a test: the figure was right and the three
// letters after it were missing, which no assertion about totals would notice.
func TestACustomersAmountAlwaysCarriesItsCurrency(t *testing.T) {
	f := newFixture(t)

	march := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

	// The most recent entry is the non-billable one, so it is the first the
	// grouping sees - the listing is newest-first.
	billableEnd := at(march, 13, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(march, 9, 0),
		EndedAt: &billableEnd, Billable: true,
	}); err != nil {
		t.Fatalf("record billable work: %v", err)
	}
	unbillableEnd := at(march.AddDate(0, 0, 1), 11, 0)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(march.AddDate(0, 0, 1), 9, 0),
		EndedAt: &unbillableEnd, Billable: false,
	}); err != nil {
		t.Fatalf("record unbillable work: %v", err)
	}

	preview, err := f.svc.PreviewExport(f.ctx, marchFilter())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.ByCustomer) != 1 {
		t.Fatalf("%d customers, want 1", len(preview.ByCustomer))
	}

	group := preview.ByCustomer[0]
	if group.AmountMinor == 0 {
		t.Fatal("the customer's amount is zero, so this proves nothing about its currency")
	}
	if group.Currency == "" {
		t.Error("the customer's block has no currency, so its amount renders as a " +
			"bare number - which is not an amount of money")
	}
	// And it is the currency the amounts were actually totalled under.
	if _, ok := preview.AmountsByCurrency[group.Currency]; !ok {
		t.Errorf("the block is in %q and the totals are in %v",
			group.Currency, preview.AmountsByCurrency)
	}
}
