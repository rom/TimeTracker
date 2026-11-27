package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Billing run storage.
//
// The service tests cover the rules. These cover the mechanism: that a run
// round-trips with its totals intact, that stamping refuses an entry another
// run already claimed, and that voiding releases what it held.

// runFixture gives a database with one entry and the ids to bill it with.
func runFixture(t *testing.T) (*DB, context.Context, domain.User, domain.Assignment) {
	t.Helper()

	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	return db, ctx, user, assignment
}

func TestABillingRunSurvivesTheRoundTrip(t *testing.T) {
	db, ctx, user, _ := runFixture(t)

	// Figures chosen to be wrong if anything round-trips through a float: an
	// amount past 2^53 minor units would not, and a nine-digit one is the
	// honest size of a quarter's invoice in minor units anyway.
	written, err := CreateBillingRunTx(ctx, db.write, domain.BillingRun{
		CustomerID: 1, StartsOn: "2026-03-01", EndsOn: "2026-03-31",
		Reference: "INV-2026-014", Note: "March",
		Currency: "SEK", EntryCount: 42, BillableSeconds: 561600,
		AmountMinor: 702000000,
		CreatedBy:   user.ID,
		CreatedAt:   time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	read, err := db.GetBillingRun(ctx, written.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if read.AmountMinor != 702000000 || read.BillableSeconds != 561600 {
		t.Errorf("totals came back as %d minor over %d seconds, want 702000000 and 561600",
			read.AmountMinor, read.BillableSeconds)
	}
	if read.Reference != "INV-2026-014" || read.Currency != "SEK" || read.EntryCount != 42 {
		t.Errorf("run came back as %+v", read)
	}
	if read.Voided() {
		t.Error("a new run reads as voided")
	}
	if read.CustomerName == "" {
		t.Error("the listing join did not fill the customer's name")
	}
	if days := read.Days(); days != 31 {
		t.Errorf("March is %d days long here, want 31", days)
	}

	runs, err := db.ListBillingRuns(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs listed, want 1", len(runs))
	}

	// A customer with nothing billed lists nothing rather than everything.
	others, err := db.ListBillingRuns(ctx, 999, 10)
	if err != nil {
		t.Fatalf("list another customer: %v", err)
	}
	if len(others) != 0 {
		t.Errorf("%d runs listed for a customer with none", len(others))
	}
}

// The race the repeated WHERE exists for: an entry another run claimed between
// the candidates being read and this run being written is not stamped, and the
// count says so.
func TestStampingSkipsTimeAnotherRunAlreadyClaimed(t *testing.T) {
	db, ctx, user, assignment := runFixture(t)

	var ids []int64
	for day := 10; day <= 12; day++ {
		entry, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt:       time.Date(2026, 3, day, 9, 0, 0, 0, time.UTC),
			DurationSeconds: 3600, Billable: true,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
			BillableSeconds: 3600, AmountMinor: 125000, Currency: "SEK",
		})
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		ids = append(ids, entry.ID)
	}

	// One of them goes out on an earlier invoice.
	stamped, err := StampEntriesTx(ctx, db.write, 1, ids[:1])
	if err != nil {
		t.Fatalf("first stamp: %v", err)
	}
	if stamped != 1 {
		t.Fatalf("stamped %d, want 1", stamped)
	}

	// A second run that still thinks it covers all three gets two, which is
	// what tells the service to refuse rather than issue a short invoice.
	stamped, err = StampEntriesTx(ctx, db.write, 2, ids)
	if err != nil {
		t.Fatalf("second stamp: %v", err)
	}
	if stamped != 2 {
		t.Errorf("stamped %d of three, want 2: the entry on the first run must "+
			"not move to the second", stamped)
	}

	// Nothing to stamp is nothing, not an error: a caller with an empty
	// selection is refused earlier, and this must not be where it blows up.
	if stamped, err = StampEntriesTx(ctx, db.write, 3, nil); err != nil || stamped != 0 {
		t.Errorf("stamping nothing returned %d, %v", stamped, err)
	}
}

func TestVoidingReleasesTheTimeItHeld(t *testing.T) {
	db, ctx, user, assignment := runFixture(t)

	entry, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt:       time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
		DurationSeconds: 3600, Billable: true,
		Status: domain.StatusConfirmed, TimeZone: "UTC",
		BillableSeconds: 3600, AmountMinor: 125000, Currency: "SEK",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	run, err := CreateBillingRunTx(ctx, db.write, domain.BillingRun{
		CustomerID: 1, StartsOn: "2026-03-01", EndsOn: "2026-03-31",
		Currency: "SEK", EntryCount: 1, BillableSeconds: 3600,
		AmountMinor: 125000, CreatedBy: user.ID,
		CreatedAt: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := StampEntriesTx(ctx, db.write, run.ID, []int64{entry.ID}); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return VoidBillingRunTx(ctx, tx, run.ID, user.ID, "2026-04-02T09:00:00Z")
	})
	if err != nil {
		t.Fatalf("void: %v", err)
	}

	after, err := db.GetBillingRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if !after.Voided() || after.VoidedBy != user.ID {
		t.Errorf("the run did not record being voided: %+v", after)
	}
	// The record survives: an invoice that was credited is still history.
	if after.AmountMinor != 125000 {
		t.Error("voiding erased what the run came to")
	}

	released, err := db.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if released.BillingRunID != 0 {
		t.Errorf("the entry still belongs to run %d after it was voided",
			released.BillingRunID)
	}
}

func TestAMissingBillingRunIsNotFound(t *testing.T) {
	db, ctx, _, _ := runFixture(t)

	if _, err := db.GetBillingRun(ctx, 404); err == nil {
		t.Error("a run that does not exist was read without error")
	}
}
