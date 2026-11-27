package service

import (
	"testing"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Reading the trail.
//
// Writing it was never the problem: every mutation has committed a row with
// its change since the first migration. Reading it was - the only query was
// for one record, the only service method had no caller outside the tests, and
// no screen, export or backup touched it. ASR-006 promises attribution, and an
// attribution nobody can look at is a promise kept in private.

func TestTheTrailSaysWhatHappenedHere(t *testing.T) {
	f := newFixture(t)

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Note: "First",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: entry.AssignmentID, StartedAt: entry.StartedAt,
		DurationSeconds: 7200, Note: "Corrected",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	page, err := f.svc.AuditLog(f.ctx, AuditQuery{})
	if err != nil {
		t.Fatalf("audit log: %v", err)
	}
	if len(page.Events) < 2 {
		t.Fatalf("%d events, want at least the create and the update", len(page.Events))
	}
	// Newest first: the question that brings somebody here is nearly always
	// about something that just happened.
	if page.Events[0].Action != "time_entry.update" {
		t.Errorf("the newest event is %q, want the update", page.Events[0].Action)
	}
	if page.Events[0].ActorName == "" {
		t.Error("an event with no actor named is not an attribution")
	}
	if page.Events[0].Detail == "" {
		t.Error("an event with no detail cannot answer what was changed")
	}

	// The filters narrow rather than decorate.
	only, err := f.svc.AuditLog(f.ctx, AuditQuery{Action: "time_entry.create"})
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	for _, event := range only.Events {
		if event.Action != "time_entry.create" {
			t.Errorf("filtering for creations returned %q", event.Action)
		}
	}
	if len(only.Events) == 0 {
		t.Error("filtering for an action that happened returned nothing")
	}

	// And the filter offers what the trail holds rather than every action the
	// code can write.
	if len(page.Actions) == 0 {
		t.Error("no actions offered for filtering")
	}
	for _, action := range page.Actions {
		if action == "" {
			t.Error("an empty action was offered")
		}
	}
}

// One record's history, which is the question asked while looking at it.
func TestARecordCarriesItsOwnHistory(t *testing.T) {
	f := newFixture(t)

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Note: "First",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: entry.AssignmentID, StartedAt: entry.StartedAt,
		DurationSeconds: 7200,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	trail, err := f.svc.AuditTrail(f.ctx, "time_entry", entry.ID)
	if err != nil {
		t.Fatalf("trail: %v", err)
	}
	if len(trail) != 2 {
		t.Fatalf("%d events for this entry, want the create and the update", len(trail))
	}
	for _, event := range trail {
		if event.ResourceID != entry.ID {
			t.Errorf("the trail of entry %d includes %s on %d",
				entry.ID, event.Action, event.ResourceID)
		}
	}
}

// The instance's history is administration. A manager's authority is bounded
// by the projects they are responsible for, and this listing is bounded by
// nothing: it names who deleted what on every project there is.
func TestTheInstancesHistoryIsNotAManagersToRead(t *testing.T) {
	f := newFixture(t)

	office, _ := officeContext(t, f)
	for _, role := range []domain.Role{domain.RoleManager, domain.RoleMember, domain.RoleClient} {
		t.Run(string(role), func(t *testing.T) {
			person, err := f.db.CreateUser(f.ctx, domain.User{
				DisplayName: "Somebody", Role: role,
				TimeZone: "UTC", Theme: "light", Active: true,
			})
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			if _, err := office.AuditLog(auth.WithUser(f.ctx, person), AuditQuery{}); err == nil {
				t.Errorf("a %s read the instance's whole history", role)
			}
		})
	}
}
