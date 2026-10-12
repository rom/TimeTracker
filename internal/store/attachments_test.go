package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Attachment metadata, and the reference counting the blob sweep depends on.
//
// The bytes live on disk under their own hash, so several records can point at
// one file: two people attaching the same receipt store it once. That makes
// deletion the interesting operation. Deleting the bytes because one reference
// went would take the file away from everybody else still pointing at it, and
// the symptom is a broken thumbnail on somebody else's expense months later.

// TestDeletingOneReferenceKeepsASharedFile.
//
// The dedup case, which is the whole reason DeleteAttachment reports whether the
// blob is now unreferenced rather than just deleting it.
func TestDeletingOneReferenceKeepsASharedFile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	end := time.Now().UTC().Add(time.Hour)
	entry, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: time.Now().UTC(), EndedAt: &end, DurationSeconds: 3600,
		Status: domain.StatusConfirmed, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	const shared = "0000000000000000000000000000000000000000000000000000000000000001"
	first, err := db.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "time_entry", OwnerID: entry.ID, SHA256: shared,
		Filename: "receipt.png", MIME: "image/png", SizeBytes: 1234, UploadedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	second, err := db.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "time_entry", OwnerID: entry.ID, SHA256: shared,
		Filename: "the-same-receipt-again.png", MIME: "image/png",
		SizeBytes: 1234, UploadedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create the second attachment: %v", err)
	}

	hash, orphaned, err := db.DeleteAttachment(ctx, first.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if hash != shared {
		t.Errorf("delete reported hash %q", hash)
	}
	if orphaned {
		t.Error("deleting one of two references reported the file as orphaned; " +
			"sweeping it would break the other one")
	}

	_, orphaned, err = db.DeleteAttachment(ctx, second.ID)
	if err != nil {
		t.Fatalf("delete the second: %v", err)
	}
	if !orphaned {
		t.Error("deleting the last reference did not report the file as orphaned, " +
			"so the bytes stay on disk forever")
	}

	if _, err := db.GetAttachment(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted attachment is still readable: %v", err)
	}
}

// TestListingAttachmentsIsScopedToOneRecord.
//
// Owner type and owner id together. Either alone is a collision waiting to
// happen: entry 4 and expense 4 both exist, and a listing keyed on the id alone
// would show a receipt from somebody's expense on an unrelated timesheet entry.
func TestListingAttachmentsIsScopedToOneRecord(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	for i, owner := range []struct {
		kind string
		id   int64
	}{{"time_entry", 4}, {"expense", 4}, {"time_entry", 5}} {
		if _, err := db.CreateAttachment(ctx, domain.Attachment{
			OwnerType: owner.kind, OwnerID: owner.id,
			SHA256:   "hash-" + string(rune('a'+i)),
			Filename: "file.png", MIME: "image/png", SizeBytes: 1, UploadedBy: user.ID,
		}); err != nil {
			t.Fatalf("create attachment: %v", err)
		}
	}

	entry4, err := db.ListAttachments(ctx, "time_entry", 4)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entry4) != 1 {
		t.Errorf("time entry 4 has %d attachments, want 1: an expense with the "+
			"same id is a different record", len(entry4))
	}
	if len(entry4) == 1 && entry4[0].OwnerType != "time_entry" {
		t.Errorf("an expense's attachment was listed on an entry: %+v", entry4[0])
	}

	none, err := db.ListAttachments(ctx, "time_entry", 999)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a record with no attachments returned %d", len(none))
	}
}

// TestReferencedHashesIsWhatTheSweepKeeps.
//
// The orphan sweep deletes every blob whose hash this does not return, so a hash
// missing from it is somebody's receipt deleted from under them. Distinct,
// because two references to one file must not make it look like two files.
func TestReferencedHashesIsWhatTheSweepKeeps(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	for _, attachment := range []struct {
		hash  string
		owner int64
	}{{"hash-one", 1}, {"hash-one", 2}, {"hash-two", 3}} {
		if _, err := db.CreateAttachment(ctx, domain.Attachment{
			OwnerType: "time_entry", OwnerID: attachment.owner, SHA256: attachment.hash,
			Filename: "f.png", MIME: "image/png", SizeBytes: 1, UploadedBy: user.ID,
		}); err != nil {
			t.Fatalf("create attachment: %v", err)
		}
	}

	hashes, err := db.ReferencedHashes(ctx)
	if err != nil {
		t.Fatalf("referenced hashes: %v", err)
	}
	if len(hashes) != 2 {
		t.Errorf("referenced hashes = %v, want two distinct ones", hashes)
	}
	for _, hash := range []string{"hash-one", "hash-two"} {
		if !hashes[hash] {
			t.Errorf("%s is referenced and was not reported; the sweep would delete "+
				"a file somebody can still see", hash)
		}
	}
}

// TestAttachmentMetadataRoundTrips.
//
// The MIME type in particular. It is determined by sniffing the bytes on the
// server rather than believing the client, and every later decision - whether to
// render it inline, which preview to build, whether to serve it at all - keys on
// what is stored here.
func TestAttachmentMetadataRoundTrips(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	created, err := db.CreateAttachment(ctx, domain.Attachment{
		OwnerType: "expense", OwnerID: 7,
		SHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Filename: "Kvitto räkning.pdf", MIME: "application/pdf",
		SizeBytes: 98765, UploadedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	if created.ID == 0 || created.CreatedAt.IsZero() {
		t.Errorf("the created attachment came back without an id or a timestamp: %+v", created)
	}

	loaded, err := db.GetAttachment(ctx, created.ID)
	if err != nil {
		t.Fatalf("get attachment: %v", err)
	}
	if loaded.Filename != "Kvitto räkning.pdf" {
		t.Errorf("filename = %q; a name with a space and a non-ASCII letter has to "+
			"survive, since it is shown to the person who uploaded it", loaded.Filename)
	}
	if loaded.MIME != "application/pdf" || loaded.SizeBytes != 98765 {
		t.Errorf("metadata was lost: %+v", loaded)
	}
	if loaded.UploadedBy != user.ID {
		t.Errorf("uploader = %d, want %d", loaded.UploadedBy, user.ID)
	}

	if _, err := db.GetAttachment(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("an attachment that does not exist: %v, want ErrNotFound", err)
	}
	if _, _, err := db.DeleteAttachment(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an attachment that does not exist: %v, want ErrNotFound", err)
	}
}
