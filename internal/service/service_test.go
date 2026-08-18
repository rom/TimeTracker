package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// fixture is a service wired to a real database in a temporary directory, with a
// clock the test controls.
//
// The store is real rather than mocked because what these tests assert -
// authorisation, audit atomicity, what counts towards a total - is partly
// enforced in SQL. A mock would only prove we called ourselves.
type fixture struct {
	svc        *Service
	db         *store.DB
	ctx        context.Context
	user       domain.User
	assignment domain.Assignment
	// now is the fixed instant the injected clock returns; tests advance it.
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("service tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &fixture{
		db:  db,
		now: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), // a Monday
	}
	// Time is injected so nothing depends on the wall clock; a test that fails
	// only at midnight is worse than no test.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.svc = New(db, auth.SingleUserAuthorizer{}, logger, func() time.Time { return f.now })

	user, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Test User", Role: domain.RoleAdmin,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.user = user
	f.ctx = auth.WithUser(ctx, user)

	customer, err := f.svc.CreateCustomer(f.ctx, domain.Customer{
		Name: "Acme", Currency: "SEK", RateMinor: 125000, // 1250.00/h
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	project, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: customer.ID, Name: "Migration", BillableDefault: true,
		RoundingRule: "up/900/0", // round up to the quarter hour
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	assignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Development", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	f.assignment = assignment
	return f
}

// advance moves the injected clock forward.
func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// TestConcurrentTimers is the service-level proof of ASR-001: several timers run
// at once, and both are recorded in full.
func TestConcurrentTimers(t *testing.T) {
	f := newFixture(t)

	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create second assignment: %v", err)
	}

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "feature work"); err != nil {
		t.Fatalf("start first: %v", err)
	}
	f.advance(30 * time.Minute)
	if _, err := f.svc.StartTimer(f.ctx, second.ID, "incident"); err != nil {
		t.Fatalf("start second: %v", err)
	}

	running, err := f.svc.RunningTimers(f.ctx)
	if err != nil {
		t.Fatalf("running timers: %v", err)
	}
	if len(running) != 2 {
		t.Fatalf("expected 2 timers running at once, got %d", len(running))
	}
}

// TestOverlappingTotals is the reason two totals exist. Two hours of work
// overlapping by one hour is two hours summed and one and a half elapsed, and
// both numbers must be reported rather than one being quietly chosen.
func TestOverlappingTotals(t *testing.T) {
	f := newFixture(t)

	// 09:00-10:00 and 09:30-10:30: two hours summed, ninety minutes elapsed.
	mustCreate(t, f, f.now, 3600)
	mustCreate(t, f, f.now.Add(30*time.Minute), 3600)

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}

	if day.Totals.SummedSeconds != 7200 {
		t.Errorf("summed = %d, want 7200", day.Totals.SummedSeconds)
	}
	if day.Totals.ElapsedSeconds != 5400 {
		t.Errorf("elapsed = %d, want 5400", day.Totals.ElapsedSeconds)
	}
	if day.Totals.OverlapSeconds != 1800 {
		t.Errorf("overlap = %d, want 1800", day.Totals.OverlapSeconds)
	}
}

// TestPendingProxyEntriesAreNotCounted is the enforcement of ASR-008: time
// recorded in someone's name does not count until they accept it.
func TestPendingProxyEntriesAreNotCounted(t *testing.T) {
	f := newFixture(t)

	colleague, err := f.db.CreateUser(context.Background(), domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create colleague: %v", err)
	}

	// Own time: counts.
	mustCreate(t, f, f.now, 3600)

	// Time proposed for someone else, written directly through the store so the
	// test does not depend on the proxy permission that arrives with the
	// multi-user model. What is under test here is the totalling rule.
	end := f.now.Add(2 * time.Hour)
	if _, err := f.db.CreateEntry(context.Background(), domain.TimeEntry{
		UserID: colleague.ID, EnteredBy: f.user.ID, AssignmentID: f.assignment.ID,
		StartedAt: f.now, EndedAt: &end, DurationSeconds: 7200,
		Status: domain.StatusPending, Billable: true, TimeZone: "UTC",
	}); err != nil {
		t.Fatalf("create proxy entry: %v", err)
	}

	entries, err := f.db.ListEntries(context.Background(), store.EntryFilter{
		Scope: store.UnrestrictedScope(),
	})
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	totals := f.svc.Totals(entries)

	if totals.SummedSeconds != 3600 {
		t.Errorf("pending proxy time leaked into the total: got %d, want 3600",
			totals.SummedSeconds)
	}
}

// TestFlaggedEntriesAreNotCounted: an entry needing review is excluded until a
// human resolves it, so a timer left running overnight cannot be billed by
// accident.
func TestFlaggedEntriesAreNotCounted(t *testing.T) {
	f := newFixture(t)

	entry := mustCreate(t, f, f.now, 3600)
	stored, err := f.db.GetEntry(context.Background(), entry.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	stored.Flagged = true
	if err := f.db.UpdateEntry(context.Background(), stored); err != nil {
		t.Fatalf("flag: %v", err)
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if day.Totals.SummedSeconds != 0 {
		t.Errorf("flagged entry counted: got %d, want 0", day.Totals.SummedSeconds)
	}
}

// TestBillingSnapshotIsStored covers ADR-0014: the rate and the rounding rule
// are recorded on the entry, so a later rate change cannot rewrite an invoiced
// amount.
func TestBillingSnapshotIsStored(t *testing.T) {
	f := newFixture(t)

	// 1h05m under a round-up-to-15-minutes rule bills 1h15m.
	entry := mustCreate(t, f, f.now, 3900)

	if entry.BillableSeconds != 4500 {
		t.Errorf("billable seconds = %d, want 4500 (rounded up to the quarter hour)",
			entry.BillableSeconds)
	}
	if entry.RoundingRuleApplied != "up/900/0" {
		t.Errorf("rounding rule not recorded on the entry: %q", entry.RoundingRuleApplied)
	}
	if entry.RateMinor != 125000 {
		t.Errorf("rate = %d, want 125000 inherited from the customer", entry.RateMinor)
	}
	// 4500 seconds at 1250.00/h is exactly 1562.50.
	if entry.AmountMinor != 156250 {
		t.Errorf("amount = %d, want 156250", entry.AmountMinor)
	}
	if entry.Currency != "SEK" {
		t.Errorf("currency = %q, want SEK", entry.Currency)
	}

	// Raising the customer's rate must not disturb what was already billed.
	customer, err := f.svc.Customer(f.ctx, 1)
	if err != nil {
		t.Fatalf("load customer: %v", err)
	}
	customer.RateMinor = 200000
	if err := f.svc.UpdateCustomer(f.ctx, customer); err != nil {
		t.Fatalf("update customer: %v", err)
	}

	reloaded, err := f.svc.Entry(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if reloaded.AmountMinor != 156250 {
		t.Errorf("an invoiced amount changed under a rate change: %d", reloaded.AmountMinor)
	}
}

// TestNonBillableEntriesCarryNoAmount: a zeroed snapshot rather than a hidden
// figure, so nothing can start billing it later by accident.
func TestNonBillableEntriesCarryNoAmount(t *testing.T) {
	f := newFixture(t)

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: false,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if entry.AmountMinor != 0 || entry.RateMinor != 0 || entry.BillableSeconds != 0 {
		t.Errorf("non-billable entry carries billing data: %+v", entry)
	}
}

// TestStopIsIdempotent: a double submit must not double a duration.
func TestStopIsIdempotent(t *testing.T) {
	f := newFixture(t)

	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	f.advance(time.Hour)

	first, err := f.svc.StopTimer(f.ctx, started.ID)
	if err != nil {
		t.Fatalf("first stop: %v", err)
	}
	f.advance(time.Hour)
	second, err := f.svc.StopTimer(f.ctx, started.ID)
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}

	if first.DurationSeconds != 3600 {
		t.Errorf("first stop recorded %d seconds, want 3600", first.DurationSeconds)
	}
	if second.DurationSeconds != first.DurationSeconds {
		t.Errorf("stopping twice changed the duration: %d then %d",
			first.DurationSeconds, second.DurationSeconds)
	}
}

// TestLongTimerIsFlaggedNotBilled: the dominant failure mode of every tracker is
// a timer left running. It must be surfaced, never silently billed.
func TestLongTimerIsFlaggedNotBilled(t *testing.T) {
	f := newFixture(t)

	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	f.advance(20 * time.Hour) // past the 12-hour default maximum

	stopped, err := f.svc.StopTimer(f.ctx, started.ID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stopped.Flagged {
		t.Error("a 20-hour timer should be flagged for review")
	}
	if stopped.Counts() {
		t.Error("a flagged entry must not count towards totals")
	}
}

// TestAuditTrailRecordsEveryMutation is the check behind ASR-006.
func TestAuditTrailRecordsEveryMutation(t *testing.T) {
	f := newFixture(t)

	entry := mustCreate(t, f, f.now, 3600)
	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 7200, Note: "corrected", Billable: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	events, err := f.svc.AuditTrail(f.ctx, "time_entry", entry.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected create and update to be audited, got %d events", len(events))
	}

	actions := map[string]bool{}
	for _, e := range events {
		actions[e.Action] = true
		if e.ActorID != f.user.ID {
			t.Errorf("audit event has the wrong actor: %+v", e)
		}
	}
	for _, want := range []string{"time_entry.create", "time_entry.update"} {
		if !actions[want] {
			t.Errorf("no audit event for %s", want)
		}
	}
}

// TestUnauthenticatedContextIsRefused: every service method resolves the actor
// from the context, so a call without one must fail rather than default to
// somebody.
func TestUnauthenticatedContextIsRefused(t *testing.T) {
	f := newFixture(t)
	bare := context.Background() // no identity

	if _, err := f.svc.StartTimer(bare, f.assignment.ID, ""); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("StartTimer without an identity: got %v, want ErrUnauthorized", err)
	}
	if _, err := f.svc.RunningTimers(bare); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("RunningTimers without an identity: got %v, want ErrUnauthorized", err)
	}
	if _, err := f.svc.Day(bare, f.now); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Day without an identity: got %v, want ErrUnauthorized", err)
	}
}

// TestAnotherUsersEntryIsNotFound: even the permissive local authoriser refuses
// records owned by someone else, and the refusal is reported as "not found" so
// identifiers cannot be probed.
func TestAnotherUsersEntryIsNotFound(t *testing.T) {
	f := newFixture(t)

	other, err := f.db.CreateUser(context.Background(), domain.User{
		DisplayName: "Someone Else", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	end := f.now.Add(time.Hour)
	theirs, err := f.db.CreateEntry(context.Background(), domain.TimeEntry{
		UserID: other.ID, EnteredBy: other.ID, AssignmentID: f.assignment.ID,
		StartedAt: f.now, EndedAt: &end, DurationSeconds: 3600,
		Status: domain.StatusConfirmed, Billable: true, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("create their entry: %v", err)
	}

	if _, err := f.svc.Entry(f.ctx, theirs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading another user's entry: got %v, want ErrNotFound", err)
	}
	if err := f.svc.DeleteEntry(f.ctx, theirs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting another user's entry: got %v, want ErrNotFound", err)
	}
}

// TestGapDetection: the day view reports uncovered stretches as prompts. Only
// the interior counts - time before the first entry is not a gap.
func TestGapDetection(t *testing.T) {
	f := newFixture(t)

	mustCreate(t, f, f.now, 3600)                                  // 09:00-10:00
	mustCreate(t, f, f.now.Add(3*time.Hour), 3600)                 // 12:00-13:00
	mustCreate(t, f, f.now.Add(4*time.Hour+5*60*time.Second), 600) // 13:05, a 5-minute break

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if len(day.Gaps) != 1 {
		t.Fatalf("expected exactly 1 reportable gap, got %d: %+v", len(day.Gaps), day.Gaps)
	}
	if day.Gaps[0].Seconds != 7200 {
		t.Errorf("gap = %d seconds, want 7200", day.Gaps[0].Seconds)
	}
}

// TestQuickAddParsing covers the two-second path, including its refusal to guess.
func TestQuickAddParsing(t *testing.T) {
	f := newFixture(t)
	// Give the matcher something to match against.
	mustCreate(t, f, f.now, 3600)

	t.Run("duration and assignment", func(t *testing.T) {
		result, err := f.svc.ParseQuickAdd(f.ctx, "2h development fixed the login redirect")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if result.Ambiguous {
			t.Fatalf("should have parsed cleanly: %s", result.Reason)
		}
		if result.Entry.DurationSeconds != 7200 {
			t.Errorf("duration = %d, want 7200", result.Entry.DurationSeconds)
		}
		if result.Assignment.ID != f.assignment.ID {
			t.Errorf("matched the wrong assignment: %+v", result.Assignment)
		}
		if result.Entry.Note != "fixed the login redirect" {
			t.Errorf("note = %q", result.Entry.Note)
		}
	})

	t.Run("tags are extracted", func(t *testing.T) {
		result, err := f.svc.ParseQuickAdd(f.ctx, "30m development standup #internal")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(result.Tags) != 1 || result.Tags[0] != "internal" {
			t.Errorf("tags = %v, want [internal]", result.Tags)
		}
		if result.Entry.Note != "standup" {
			t.Errorf("tag left in the note: %q", result.Entry.Note)
		}
	})

	t.Run("unmatched assignment is ambiguous, not guessed", func(t *testing.T) {
		result, err := f.svc.ParseQuickAdd(f.ctx, "2h somethingnobodyhas did work")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !result.Ambiguous {
			t.Errorf("should not have guessed an assignment: matched %+v", result.Assignment)
		}
	})

	t.Run("no duration is ambiguous", func(t *testing.T) {
		result, err := f.svc.ParseQuickAdd(f.ctx, "development some work")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !result.Ambiguous {
			t.Error("a line with no duration should not create an entry")
		}
	})
}

// mustCreate adds a confirmed billable entry and fails the test if it cannot.
func mustCreate(t *testing.T, f *fixture, start time.Time, seconds int64) domain.TimeEntry {
	t.Helper()
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID:    f.assignment.ID,
		StartedAt:       start,
		DurationSeconds: seconds,
		Billable:        true,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	return entry
}
