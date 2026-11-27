package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Undoing a deletion.
//
// The property worth protecting is not "there is a bin" but "a deletion puts
// back exactly what was taken". A restore that loses the tags, the proxy
// subject or the billing snapshot is worse than no restore at all: it looks
// like a recovery and quietly changes an invoiced figure.

// TestADeletedEntryComesBackWholly.
func TestADeletedEntryComesBackWholly(t *testing.T) {
	f := newFixture(t)

	created, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 5400, Note: "the thing that was deleted",
		Tags: []string{"incident", "urgent"}, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := f.svc.Entry(f.ctx, created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if err := f.svc.DeleteEntry(f.ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.svc.Entry(f.ctx, created.ID); err == nil {
		t.Fatal("the entry is still readable after being deleted")
	}

	bin, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil {
		t.Fatalf("list bin: %v", err)
	}
	if len(bin) != 1 {
		t.Fatalf("the bin holds %d records after one deletion, want 1", len(bin))
	}
	// The summary is what somebody reads when choosing what to put back, so it
	// has to name the work rather than the row.
	if !strings.Contains(bin[0].Summary, "the thing that was deleted") {
		t.Errorf("the bin entry does not say what was deleted: %q", bin[0].Summary)
	}

	if err := f.svc.RestoreDeleted(f.ctx, bin[0].ID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	after, err := f.svc.Entry(f.ctx, created.ID)
	if err != nil {
		t.Fatalf("the entry did not come back: %v", err)
	}

	// Field by field, because the failure this guards against is a restore
	// that silently drops one. The identity matters as much as the values:
	// anything that referred to this entry by id still does.
	if after.ID != before.ID {
		t.Errorf("restored under id %d, was %d", after.ID, before.ID)
	}
	if after.DurationSeconds != before.DurationSeconds {
		t.Errorf("duration = %d, want %d", after.DurationSeconds, before.DurationSeconds)
	}
	if !after.StartedAt.Equal(before.StartedAt) {
		t.Errorf("started at %v, want %v", after.StartedAt, before.StartedAt)
	}
	if after.Note != before.Note {
		t.Errorf("note = %q, want %q", after.Note, before.Note)
	}
	if after.Billable != before.Billable {
		t.Errorf("billable = %v, want %v", after.Billable, before.Billable)
	}
	// The billing snapshot, which is the one an invoice was written from.
	if after.AmountMinor != before.AmountMinor || after.RateMinor != before.RateMinor {
		t.Errorf("billing snapshot came back as %d at %d, was %d at %d",
			after.AmountMinor, after.RateMinor, before.AmountMinor, before.RateMinor)
	}
	// And the tags, which live in a second table and are the thing a
	// single-row capture would have lost.
	if len(after.Tags) != len(before.Tags) {
		t.Errorf("tags = %v, want %v", after.Tags, before.Tags)
	}

	// The bin is empty again: a restored record is not still restorable.
	if bin, err = f.svc.RecentlyDeleted(f.ctx); err != nil || len(bin) != 0 {
		t.Errorf("after restoring, the bin holds %d records (err %v), want 0", len(bin), err)
	}
}

// TestRestoringIntoALockedWeekIsRefused.
//
// The bin does not get to bypass the week lock. An entry deleted while the week
// was open and restored after it was approved would put hours back into figures
// somebody has already signed off, which is the exact thing ADR-0023 exists to
// prevent - and it would do it without the visible, audited reopen.
func TestRestoringIntoALockedWeekIsRefused(t *testing.T) {
	f := newFixture(t)

	// Two entries, because a week with nothing in it cannot be submitted - and
	// the week has to be submitted for this test to have anything to say.
	created, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Note: "deleted before the week was submitted",
		Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now.Add(2 * time.Hour),
		DurationSeconds: 3600, Note: "what the week was submitted with",
		Billable: true,
	}); err != nil {
		t.Fatalf("create the second: %v", err)
	}

	if err := f.svc.DeleteEntry(f.ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	bin, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil || len(bin) != 1 {
		t.Fatalf("bin = %d records, err %v", len(bin), err)
	}
	if err := f.svc.RestoreDeleted(f.ctx, bin[0].ID); !errors.Is(err, domain.ErrPeriodLocked) {
		t.Errorf("restoring into a submitted week = %v, want a locked-period refusal", err)
	}
}

// TestTheBinIsNotEverybodysToReadOrRestore.
//
// A deletion belongs to the person whose record it was. Listing somebody else's
// would say what they recorded and when they thought better of it, which is not
// a thing the bin is for.
func TestTheBinIsNotEverybodysToReadOrRestore(t *testing.T) {
	f := newFixture(t)

	created, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Note: "mine", Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.DeleteEntry(f.ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	mine, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil || len(mine) != 1 {
		t.Fatalf("bin = %d records, err %v", len(mine), err)
	}

	// Somebody else, with no claim on it.
	colleague, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create colleague: %v", err)
	}
	theirs := auth.WithUser(f.ctx, colleague)

	listed, err := f.svc.RecentlyDeleted(theirs)
	if err != nil {
		t.Fatalf("list as colleague: %v", err)
	}
	for _, record := range listed {
		if record.ID == mine[0].ID {
			t.Error("a colleague can read somebody else's deletions")
		}
	}
}

// TestADeletedTagBelongsToTheInstance.
//
// A tag is shared by every project and deleting one takes it off entries
// everywhere, so it is binned with no owner and only somebody who may
// administer the instance can put it back - the same bound that let them
// delete it (ADR-0008). The entry links travel with it: a vocabulary word
// restored without the work that carried it is a different tag with the same
// name.
func TestADeletedTagBelongsToTheInstance(t *testing.T) {
	f := newFixture(t)

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Tags: []string{"incident"}, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tags, err := f.svc.TagsForAdmin(f.ctx)
	if err != nil || len(tags) == 0 {
		t.Fatalf("tags = %d, err %v", len(tags), err)
	}
	if err := f.svc.DeleteTag(f.ctx, tags[0].ID); err != nil {
		t.Fatalf("delete tag: %v", err)
	}

	// Off the entry.
	stripped, err := f.svc.Entry(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if len(stripped.Tags) != 0 {
		t.Fatalf("the entry still carries %v after the tag was deleted", stripped.Tags)
	}

	bin, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil || len(bin) != 1 {
		t.Fatalf("bin = %d records, err %v", len(bin), err)
	}
	if bin[0].UserID != 0 {
		t.Errorf("the tag was binned against user %d; a tag belongs to the "+
			"instance, not to whoever happened to delete it", bin[0].UserID)
	}

	if err := f.svc.RestoreDeleted(f.ctx, bin[0].ID); err != nil {
		t.Fatalf("restore tag: %v", err)
	}

	// Back on the entry, which is the half a single-table capture would lose.
	restored, err := f.svc.Entry(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if len(restored.Tags) != 1 || restored.Tags[0] != "incident" {
		t.Errorf("the restored tag is not back on its entry: %v", restored.Tags)
	}
}

// TestEveryBinnedKindCanBeRestored.
//
// binOrder is both the write-back order and the allow-list, and a kind binned
// without an entry here is a record that goes into the bin and cannot come out.
// Checked against the kinds the service actually bins rather than against
// itself, so adding a sixth deletion without a table is caught here.
func TestEveryBinnedKindCanBeRestored(t *testing.T) {
	for _, kind := range []string{
		"time_entry", "expense", "tag", "routine", "contract_term",
	} {
		tables, known := binOrder[kind]
		if !known {
			t.Errorf("%s is binned but binOrder has no write-back order for it, "+
				"so it would go in and never come out", kind)
			continue
		}
		if len(tables) == 0 {
			t.Errorf("%s has an empty write-back order", kind)
		}
	}
}

// TestADeletionIsStillAudited.
//
// The bin is not a replacement for the trail. The audit row still says who
// deleted what and when, because the bin answers "can I have it back" and the
// trail answers "who did this", and the second question outlives the first.
func TestADeletionIsStillAudited(t *testing.T) {
	f := newFixture(t)

	created, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.DeleteEntry(f.ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	trail, err := f.svc.AuditTrail(f.ctx, "time_entry", created.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	var deleted bool
	for _, row := range trail {
		if row.Action == "time_entry.delete" {
			deleted = true
		}
	}
	if !deleted {
		t.Error("deleting an entry no longer writes an audit row; the bin is " +
			"meant to sit beside the trail, not replace it")
	}

	// And restoring is audited too, so a figure that came back has a record of
	// having done so.
	bin, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil || len(bin) != 1 {
		t.Fatalf("bin = %d, err %v", len(bin), err)
	}
	if err := f.svc.RestoreDeleted(f.ctx, bin[0].ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if trail, err = f.svc.AuditTrail(f.ctx, "time_entry", created.ID); err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	var restored bool
	for _, row := range trail {
		if row.Action == "time_entry.restore" {
			restored = true
		}
	}
	if !restored {
		t.Error("restoring a record is not audited")
	}
}
