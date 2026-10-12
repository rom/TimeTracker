package service

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/archive"
	"github.com/rom/timetracker/internal/domain"
)

// Restoring an archive whose attachments are not what it says they are.
//
// Every attachment in an archive carries the SHA-256 it was stored under, and
// the restore checks the bytes against it. That check is the difference
// between a backup and a hope: without it, a receipt corrupted in transit or
// on a failing disk comes back silently, under the right filename, as evidence
// for a billed expense. Nobody opens an old receipt, so nobody would find out.
//
// The archives here are built by writing a real one and then rewriting its
// zip, since nothing in the application will produce a damaged archive on
// purpose.

// rewriteArchive copies an archive, passing each file's bytes through a
// function that may change or drop them.
//
// It writes the copy unencrypted, which is what the fixture's instances take -
// none of them has a backup password set.
func rewriteArchive(t *testing.T, original []byte, change func(name string, data []byte) ([]byte, bool)) []byte {
	t.Helper()

	reader, err := archive.NewReader(bytes.NewReader(original), int64(len(original)), "")
	if err != nil {
		t.Fatalf("read the original archive: %v", err)
	}

	var out bytes.Buffer
	writer := archive.NewWriter(&out, "")
	for _, name := range reader.Names() {
		data, err := reader.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		replacement, keep := change(name, data)
		if !keep {
			continue
		}
		if err := writer.Add(name, time.Now(), bytes.NewReader(replacement)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close the rewritten archive: %v", err)
	}
	return out.Bytes()
}

// archiveWithAReceipt is a real archive carrying one entry and one attachment.
func archiveWithAReceipt(t *testing.T) []byte {
	t.Helper()
	source := withBlobs(t, newFixture(t))
	entry := mustCreate(t, source, source.now, 3600)
	if _, err := source.svc.Attach(source.ctx, domain.AttachmentOwnerTimeEntry,
		entry.ID, "receipt.txt", strings.NewReader("the original receipt")); err != nil {
		t.Fatalf("attach: %v", err)
	}
	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return buf.Bytes()
}

// TestAReceiptWhoseBytesHaveChangedIsRefusedAndReported.
//
// The hash check. The records still come back - a corrupt receipt must not
// cost a year of timesheets - and the file does not, which is said in words
// naming the file so somebody can go and find the original.
func TestAReceiptWhoseBytesHaveChangedIsRefusedAndReported(t *testing.T) {
	original := archiveWithAReceipt(t)

	tampered := rewriteArchive(t, original, func(name string, data []byte) ([]byte, bool) {
		if strings.HasPrefix(name, ArchiveAttachmentDir) {
			return []byte("a different receipt entirely"), true
		}
		return data, true
	})

	target := withBlobs(t, newFixture(t))
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(tampered), int64(len(tampered)))
	if err != nil {
		t.Fatalf("the restore gave up rather than reporting: %v", err)
	}

	if result.Entries == 0 {
		t.Error("no entries were restored; a corrupt receipt must not cost the " +
			"records around it")
	}
	if result.Attachments != 0 {
		t.Errorf("restored %d attachments whose bytes did not match their hash - "+
			"a corrupt receipt came back as evidence for a billed expense",
			result.Attachments)
	}
	problems := strings.Join(result.Problems, "\n")
	if !strings.Contains(problems, "receipt.txt") {
		t.Errorf("the problems do not name the file that failed its check:\n%s", problems)
	}
}

// TestAnArchiveMissingAFileItListsIsReported.
//
// The document says there is an attachment at a path and the zip has nothing
// there, which is what a truncated transfer or a hand-edited archive looks
// like. The same reasoning as a changed file: the records arrive, the absence
// is named.
func TestAnArchiveMissingAFileItListsIsReported(t *testing.T) {
	original := archiveWithAReceipt(t)

	gutted := rewriteArchive(t, original, func(name string, data []byte) ([]byte, bool) {
		return data, !strings.HasPrefix(name, ArchiveAttachmentDir)
	})

	target := withBlobs(t, newFixture(t))
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(gutted), int64(len(gutted)))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Entries == 0 {
		t.Error("no entries were restored")
	}
	if result.Attachments != 0 {
		t.Errorf("restored %d attachments from an archive that carries none",
			result.Attachments)
	}
	if len(result.Problems) == 0 {
		t.Error("an archive missing a file it lists reported no problem, so a " +
			"billed expense comes back with no receipt and nobody is told")
	}
}

// TestAnArchiveWithNoBackupDocumentIsNotAnArchive.
//
// A zip of something else entirely - somebody's photos, or a release
// tarball renamed. It has to be refused by name rather than restored as an
// empty backup, which would look like a successful restore of nothing.
func TestAnArchiveWithNoBackupDocumentIsNotAnArchive(t *testing.T) {
	original := archiveWithAReceipt(t)

	withoutDocument := rewriteArchive(t, original, func(name string, data []byte) ([]byte, bool) {
		return data, name != ArchiveDocument
	})

	target := withBlobs(t, newFixture(t))
	_, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(withoutDocument), int64(len(withoutDocument)))
	if err == nil {
		t.Fatal("a zip with no backup document in it was restored")
	}
	if !strings.Contains(err.Error(), ArchiveDocument) {
		t.Errorf("the refusal reads %q, which does not say what it was looking for", err)
	}

	// A zip of unrelated files is the same answer.
	var unrelated bytes.Buffer
	writer := zip.NewWriter(&unrelated)
	part, err := writer.Create("holiday.jpg")
	if err != nil {
		t.Fatalf("build an unrelated zip: %v", err)
	}
	if _, err := part.Write([]byte("not a backup")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(unrelated.Bytes()), int64(unrelated.Len())); err == nil {
		t.Error("a zip of unrelated files was restored as a backup")
	}
}

// TestAnArchiveWithACorruptDocumentIsRefused.
//
// The zip reads and the document inside it does not. Nothing may be written:
// a restore that applied the half of a document it could parse would leave
// somebody reconciling two sources, which is the outcome the all-or-nothing
// refusal exists to avoid.
func TestAnArchiveWithACorruptDocumentIsRefused(t *testing.T) {
	original := archiveWithAReceipt(t)

	broken := rewriteArchive(t, original, func(name string, data []byte) ([]byte, bool) {
		if name == ArchiveDocument {
			return []byte(`{"format_version": 1, "customers": [{"name":`), true
		}
		return data, true
	})

	target := withBlobs(t, newFixture(t))
	before, err := target.svc.Customers(target.ctx, true)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}

	if _, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(broken), int64(len(broken))); err == nil {
		t.Fatal("an archive with an unparseable document was restored")
	}

	after, err := target.svc.Customers(target.ctx, true)
	if err != nil {
		t.Fatalf("re-list customers: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the catalogue went from %d customers to %d; a refused restore "+
			"must write nothing", len(before), len(after))
	}
}

// TestARestoredReceiptIsTheSameBytes.
//
// The positive case, which is what makes the checks above meaningful: an
// archive restored into a fresh instance gives back a receipt whose contents
// are what was attached, not merely a file of the right name and size.
func TestARestoredReceiptIsTheSameBytes(t *testing.T) {
	original := archiveWithAReceipt(t)

	target := withBlobs(t, newFixture(t))
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Attachments != 1 {
		t.Fatalf("restored %d attachments, want 1 (problems: %v)",
			result.Attachments, result.Problems)
	}

	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries were restored")
	}

	attachments, err := target.svc.Attachments(target.ctx,
		domain.AttachmentOwnerTimeEntry, entries[0].ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("%d attachments on the restored entry, want 1", len(attachments))
	}
	if attachments[0].Filename != "receipt.txt" {
		t.Errorf("the restored receipt is named %q", attachments[0].Filename)
	}

	_, reader, err := target.svc.OpenAttachment(target.ctx, attachments[0].ID)
	if err != nil {
		t.Fatalf("open the restored receipt: %v", err)
	}
	defer func() { _ = reader.Close() }()
	var content bytes.Buffer
	if _, err := content.ReadFrom(reader); err != nil {
		t.Fatalf("read the restored receipt: %v", err)
	}
	if content.String() != "the original receipt" {
		t.Errorf("the restored receipt reads %q, want the bytes that were attached",
			content.String())
	}
}
