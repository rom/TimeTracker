package service

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// The narrowed client projection (ADR-0008).
//
// These tests are written from the position that the interface will eventually
// render whatever the service hands it. So they do not check that a screen hides
// a note; they check that the note is not in the value, which is the guarantee
// the ADR made and the only one a template bug cannot undo.

// clientFixture returns the fixture with a client user for its customer, and a
// service behind the real authoriser.
func clientFixture(t *testing.T, f *fixture) *fixture {
	t.Helper()

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	client, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "The Client", Role: domain.RoleClient, TimeZone: "UTC",
		Theme: "light", Active: true, ClientCustomerID: project.CustomerID,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(f.db, auth.RoleAuthorizer{IsProjectMember: f.db.IsProjectMember}, logger,
		func() time.Time { return f.now })
	return &fixture{db: f.db, svc: svc, now: f.now, ctx: auth.WithUser(f.ctx, client),
		assignment: f.assignment, user: client}
}

// officeContext runs the fixture's own admin behind the real authoriser.
//
// The fixture's service uses the local authoriser, which refuses to act on
// another user's records at all - the right rule for one person on a laptop, and
// the wrong one for setting up a proxy proposal to project away.
func officeContext(t *testing.T, f *fixture) (*Service, context.Context) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(f.db, auth.RoleAuthorizer{IsProjectMember: f.db.IsProjectMember}, logger,
		func() time.Time { return f.now })
	return svc, auth.WithUser(f.ctx, f.user)
}

// wholeRange is a filter covering the fixture's data.
func wholeRange(f *fixture) EntryFilter {
	return EntryFilter{From: f.now.AddDate(0, 0, -30), To: f.now.AddDate(0, 0, 30)}
}

// TestClientCanSeeTheirCustomersWork.
//
// The first thing to establish, because it was not true: the role existed, the
// scope existed, and the authorisation check asked whether a client owned the
// record - so a client could sign in and read nothing at all. An empty portal is
// not a narrowed projection.
func TestClientCanSeeTheirCustomersWork(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3 * 3600, Billable: true, Note: "migration work",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	client := clientFixture(t, f)
	entries, err := client.svc.Entries(client.ctx, wholeRange(f))
	if err != nil {
		t.Fatalf("a client reading their customer's work: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1", len(entries))
	}
	// What they are entitled to: what was done, for how long, by whom.
	entry := entries[0]
	if entry.DurationSeconds != 3*3600 {
		t.Errorf("duration = %ds, want 3h", entry.DurationSeconds)
	}
	if entry.AssignmentName == "" || entry.ProjectName == "" || entry.CustomerName == "" {
		t.Error("a client should be told which work this was")
	}
	if entry.UserName == "" {
		t.Error("a client should be told who did the work")
	}
}

// TestClientProjectionRemovesWhatItPromised.
//
// The list is the ADR's, item for item. Each of these is a field somebody could
// otherwise reach by looking at a page's source, an export, or a JSON document.
func TestClientProjectionRemovesWhatItPromised(t *testing.T) {
	f := newFixture(t)

	colleague, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember, TimeZone: "UTC",
		Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create colleague: %v", err)
	}
	// Proposing time for somebody takes a shared project, which is what makes
	// the proxy workflow safe (ADR-0005).
	if err := f.db.AddProjectMember(f.ctx, store.ProjectMember{
		ProjectID: f.assignment.ProjectID, UserID: colleague.ID,
	}); err != nil {
		t.Fatalf("add colleague to the project: %v", err)
	}
	// A proxy entry, so the projection has an "entered by" to remove, priced so
	// it has money to remove, tagged and noted.
	office, officeCtx := officeContext(t, f)
	entry, err := office.CreateEntry(officeCtx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3600, Billable: true,
		OnBehalfOf: colleague.ID,
		Note:       "chasing them again about the unpaid invoice",
		Tags:       []string{"difficult"},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	// Accept it, so it counts and is visible at all.
	if _, err := office.AcceptEntry(auth.WithUser(f.ctx, colleague), entry.ID); err != nil {
		t.Fatalf("accept proposal: %v", err)
	}

	// What the office sees, for comparison. Read by id rather than listed,
	// because the entry belongs to the colleague and a listing defaults to the
	// asker's own timesheet.
	full, err := office.Entry(officeCtx, entry.ID)
	if err != nil {
		t.Fatalf("office view: %v", err)
	}
	if full.Note == "" || full.AmountMinor == 0 || full.EnteredByName == "" || len(full.Tags) == 0 {
		t.Fatalf("the fixture did not produce an entry with anything to remove: %+v", full)
	}

	client := clientFixture(t, f)
	entries, err := client.svc.Entries(client.ctx, wholeRange(f))
	if err != nil {
		t.Fatalf("client view: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1", len(entries))
	}

	got := entries[0]
	for _, check := range []struct {
		what string
		bad  bool
	}{
		{"the internal note", got.Note != ""},
		{"the hourly rate", got.RateMinor != 0},
		{"the amount", got.AmountMinor != 0},
		{"the currency", got.Currency != ""},
		{"the rounding rule", got.RoundingRuleApplied != ""},
		{"who entered it", got.EnteredByName != "" || got.EnteredBy != 0},
		{"the decision on the proposal", got.DecidedBy != 0 || !got.DecidedAt.IsZero()},
		{"the tags", len(got.Tags) != 0},
		{"the attachment count", got.AttachmentCount != 0},
	} {
		if check.bad {
			t.Errorf("a client can see %s", check.what)
		}
	}
}

// TestClientSeesOnlyConfirmedWork.
//
// A proposal nobody has accepted, and an entry flagged for review, are not work
// anybody has agreed happened. Scoping to a customer does not exclude them - the
// query has to - and a client asking what was done for them this month must not
// be shown either.
func TestClientSeesOnlyConfirmedWork(t *testing.T) {
	f := newFixture(t)

	colleague, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember, TimeZone: "UTC",
		Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create colleague: %v", err)
	}
	if err := f.db.AddProjectMember(f.ctx, store.ProjectMember{
		ProjectID: f.assignment.ProjectID, UserID: colleague.ID,
	}); err != nil {
		t.Fatalf("add colleague to the project: %v", err)
	}
	// Left pending: proposed for a colleague and never accepted.
	office, officeCtx := officeContext(t, f)
	if _, err := office.CreateEntry(officeCtx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3600, Billable: true, OnBehalfOf: colleague.ID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	// Flagged for review.
	flagged, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 13, 0),
		DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	stored, err := f.db.GetEntry(f.ctx, flagged.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	stored.Flagged = true
	if err := f.db.UpdateEntry(f.ctx, stored); err != nil {
		t.Fatalf("flag: %v", err)
	}
	// And one that counts.
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 15, 0),
		DurationSeconds: 1800, Billable: true, Note: "the real one",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	client := clientFixture(t, f)
	entries, err := client.svc.Entries(client.ctx, wholeRange(f))
	if err != nil {
		t.Fatalf("client view: %v", err)
	}
	if len(entries) != 1 {
		for _, e := range entries {
			t.Logf("visible: status=%s flagged=%v %ds", e.Status, e.Flagged, e.DurationSeconds)
		}
		t.Fatalf("%d entries visible to the client, want only the confirmed one", len(entries))
	}
	if entries[0].DurationSeconds != 1800 {
		t.Errorf("the wrong entry survived: %ds", entries[0].DurationSeconds)
	}

	// The count behind a pager has to agree, or "1 of 3" is a claim about rows
	// nobody can reach.
	count, err := client.svc.CountEntries(client.ctx, wholeRange(f))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 - the pager must count what can be shown", count)
	}
}

// TestClientSeesNothingOfAnotherCustomer.
//
// The scope, from the other side. A client with a customer set sees that
// customer; a client with none set sees nothing rather than everything, which is
// the failure a misconfigured account should have.
func TestClientSeesNothingOfAnotherCustomer(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Globex", Currency: "SEK"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	stranger, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Other Client", Role: domain.RoleClient, TimeZone: "UTC",
		Theme: "light", Active: true, ClientCustomerID: other.ID,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	unconfigured, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Unconfigured", Role: domain.RoleClient, TimeZone: "UTC",
		Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(f.db, auth.RoleAuthorizer{IsProjectMember: f.db.IsProjectMember}, logger,
		func() time.Time { return f.now })

	if entries, err := svc.Entries(auth.WithUser(f.ctx, stranger), wholeRange(f)); err != nil || len(entries) != 0 {
		t.Errorf("another customer's client sees %d entries (err %v), want 0", len(entries), err)
	}
	// A client with no customer is refused outright rather than shown
	// everything: the authoriser has nothing to scope them to.
	if _, err := svc.Entries(auth.WithUser(f.ctx, unconfigured), wholeRange(f)); err == nil {
		t.Error("a client with no customer should be refused, not given the instance")
	}
}

// TestClientExportIsNarrowedToo.
//
// The export is where forgetting would be least visible: nobody reads it on
// screen first, and the file is the thing that gets forwarded.
func TestClientExportIsNarrowedToo(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3600, Billable: true,
		Note: "internal: they still have not paid for March",
		Tags: []string{"chase"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	client := clientFixture(t, f)
	var seen int
	for entry, err := range client.svc.EachEntry(client.ctx, wholeRange(f)) {
		if err != nil {
			t.Fatalf("streaming: %v", err)
		}
		seen++
		if entry.Note != "" || entry.AmountMinor != 0 || len(entry.Tags) != 0 {
			t.Errorf("the streamed export carries what the listing removed: %+v", entry)
		}
	}
	if seen != 1 {
		t.Errorf("%d streamed entries, want 1", seen)
	}
}

// TestClientCannotTakeABackup.
//
// A backup is an administrative artefact of the whole instance, and it carries
// the catalogue - including the customer's negotiated hourly rate. Before this
// it was permitted: the check asked whether the actor could view their
// customer's time, which a client can, and the archive came back with the rate
// in it. That is exactly the cost data the projection exists to withhold, so the
// route is closed rather than narrowed - a client's way to their own data is the
// export.
func TestClientCannotTakeABackup(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 3600, Billable: true, Note: "internal",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	client := clientFixture(t, f)

	if _, err := client.svc.CreateBackup(client.ctx, BackupOptions{}); err == nil {
		t.Error("a client took a backup of the instance")
	}
	var archive bytes.Buffer
	if err := client.svc.WriteArchive(client.ctx, &archive, BackupOptions{}); err == nil {
		t.Errorf("a client downloaded a %d-byte archive", archive.Len())
	}

	// And the office still can, so the refusal is about the role rather than
	// about the feature.
	office, officeCtx := officeContext(t, f)
	if _, err := office.CreateBackup(officeCtx, BackupOptions{}); err != nil {
		t.Errorf("the refusal caught an administrator too: %v", err)
	}
}
