package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Reading the trail, at the level where the filters are.
//
// The service decides who may read it. This decides whether the question asked
// is the question answered - and these are the filters somebody uses when
// something has gone wrong and they are looking for one row in a year of them.

// trailFixture writes a handful of events across two actors and two days.
func trailFixture(t *testing.T) (*DB, context.Context) {
	t.Helper()

	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	events := []AuditEvent{
		{At: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), ActorID: user.ID,
			ActorName: "Alex", Action: "time_entry.create", ResourceType: "time_entry", ResourceID: 1},
		{At: time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC), ActorID: user.ID,
			ActorName: "Alex", Action: "time_entry.update", ResourceType: "time_entry", ResourceID: 1},
		{At: time.Date(2026, 3, 18, 9, 0, 0, 0, time.UTC), ActorID: 99,
			ActorName: "Sam", Action: "customer.create", ResourceType: "customer", ResourceID: 2},
	}
	for _, e := range events {
		if err := db.InTx(ctx, func(tx *sql.Tx) error {
			return InsertAuditTx(ctx, tx, e)
		}); err != nil {
			t.Fatalf("write audit event: %v", err)
		}
	}
	return db, ctx
}

func TestTheTrailIsNewestFirstAndFilterable(t *testing.T) {
	db, ctx := trailFixture(t)

	all, err := db.ListAuditTrail(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("%d events, want 3", len(all))
	}
	if all[0].Action != "customer.create" {
		t.Errorf("the first event is %q, want the newest", all[0].Action)
	}

	for _, c := range []struct {
		name   string
		filter AuditFilter
		want   int
	}{
		{"by actor", AuditFilter{ActorID: 99}, 1},
		{"by action", AuditFilter{Action: "time_entry.update"}, 1},
		{"by resource type", AuditFilter{ResourceType: "time_entry"}, 2},
		{"from a day", AuditFilter{From: time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)}, 1},
		{"to a day, exclusive", AuditFilter{To: time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)}, 2},
		{"a day in the middle", AuditFilter{
			From: time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC),
		}, 2},
		{"two conditions at once", AuditFilter{ActorID: 99, Action: "time_entry.create"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := db.ListAuditTrail(ctx, c.filter)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != c.want {
				t.Errorf("%d events, want %d", len(got), c.want)
			}
		})
	}
}

func TestTheTrailPagesWithoutCountingItself(t *testing.T) {
	db, ctx := trailFixture(t)

	first, err := db.ListAuditTrail(ctx, AuditFilter{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("%d events on a page of two", len(first))
	}
	second, err := db.ListAuditTrail(ctx, AuditFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("%d events on the second page, want 1", len(second))
	}
	if second[0].ID == first[0].ID {
		t.Error("the second page repeats the first")
	}
}

func TestTheFilterOffersTheActionsTheTrailHolds(t *testing.T) {
	db, ctx := trailFixture(t)

	actions, err := db.AuditActions(ctx)
	if err != nil {
		t.Fatalf("actions: %v", err)
	}
	// Three events, two of them creations of different things, one update:
	// three distinct actions, each offered once.
	if len(actions) != 3 {
		t.Errorf("%d actions offered, want 3 distinct ones: %v", len(actions), actions)
	}
	seen := map[string]bool{}
	for _, action := range actions {
		if seen[action] {
			t.Errorf("%q offered twice", action)
		}
		seen[action] = true
	}
}

// Every tag, used or not. The screen's listing joins to usage and is scoped;
// a restore asks the different question of what this instance already knows.
func TestEveryTagNameIsListedUsedOrNot(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.InTx(ctx, func(tx *sql.Tx) error {
		_, err := EnsureTagsTx(ctx, tx, []string{"incident", "migration"})
		return err
	}); err != nil {
		t.Fatalf("ensure tags: %v", err)
	}

	names, err := db.AllTagNames(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("%d tags, want 2: %v", len(names), names)
	}
	// Normalised on the way in, which is what makes the restore's "do we
	// already know this one" comparison answerable.
	for _, name := range names {
		if name != domain.NormaliseTag(name) {
			t.Errorf("%q is stored unnormalised", name)
		}
	}
}
