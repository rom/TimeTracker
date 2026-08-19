package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/archive"
	"github.com/rom/timetracker/internal/blob"
	"github.com/rom/timetracker/internal/domain"
)

// withBlobs gives a fixture somewhere to put attachments.
func withBlobs(t *testing.T, f *fixture) *fixture {
	t.Helper()
	store, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	f.svc = f.svc.WithBlobs(store)
	return f
}

// TestArchiveCarriesAttachments is the point of the whole format change: a
// receipt is the evidence behind a billed expense, and a backup that saved the
// row and lost the photograph saved the wrong half.
func TestArchiveCarriesAttachments(t *testing.T) {
	source := withBlobs(t, newFixture(t))

	entry := mustCreate(t, source, source.now, 3600)
	const receipt = "not really a PDF, but it is bytes and it is mine"
	if _, err := source.svc.Attach(source.ctx, "time_entry", entry.ID,
		"receipt.txt", strings.NewReader(receipt)); err != nil {
		t.Fatalf("attach: %v", err)
	}

	expense, err := source.svc.CreateExpense(source.ctx, ExpenseInput{
		ProjectID: source.assignment.ProjectID, SpentOn: "2026-03-16",
		Description: "Taxi to the client", Amount: "425.00", Billable: true,
		Reimbursable: true,
	})
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}
	// A real JPEG signature: the blob store checks the contents against the
	// extension on the way in, and rightly refuses a text file called .jpg.
	taxi := "\xff\xd8\xff\xe0 a photograph of a taxi receipt \xff\xd9"
	if _, err := source.svc.Attach(source.ctx, "expense", expense.ID,
		"taxi.jpg", strings.NewReader(taxi)); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	// A separate instance, with its own blob store.
	target := withBlobs(t, newFixture(t))
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(result.Problems) > 0 {
		t.Fatalf("restore reported problems: %v", result.Problems)
	}
	if result.Attachments != 2 {
		t.Fatalf("restored %d attachments, want 2", result.Attachments)
	}
	if result.Expenses != 1 {
		t.Fatalf("restored %d expenses, want 1", result.Expenses)
	}

	// The bytes have to come back, attached to the right record.
	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	assertAttachment(t, target, "time_entry", entries[0].ID, "receipt.txt", receipt)

	expenses, err := target.svc.Expenses(target.ctx, ExpenseFilter{})
	if err != nil {
		t.Fatalf("expenses: %v", err)
	}
	if len(expenses) != 1 {
		t.Fatalf("got %d expenses, want 1", len(expenses))
	}
	if expenses[0].Description != "Taxi to the client" {
		t.Errorf("description = %q", expenses[0].Description)
	}
	// The billed figure is restored, not recomputed: a claim is worth what it
	// was worth when it was made.
	if expenses[0].AmountMinor != 42500 {
		t.Errorf("amount = %d minor units, want 42500", expenses[0].AmountMinor)
	}
	assertAttachment(t, target, "expense", expenses[0].ID, "taxi.jpg", taxi)
}

func assertAttachment(t *testing.T, f *fixture, ownerType string, ownerID int64, filename, want string) {
	t.Helper()
	attachments, err := f.svc.Attachments(f.ctx, ownerType, ownerID)
	if err != nil {
		t.Fatalf("attachments for %s %d: %v", ownerType, ownerID, err)
	}
	if len(attachments) != 1 {
		t.Fatalf("%s %d has %d attachments, want 1", ownerType, ownerID, len(attachments))
	}
	if attachments[0].Filename != filename {
		t.Errorf("filename = %q, want %q", attachments[0].Filename, filename)
	}

	_, reader, err := f.svc.OpenAttachment(f.ctx, attachments[0].ID)
	if err != nil {
		t.Fatalf("open attachment: %v", err)
	}
	defer func() { _ = reader.Close() }()

	var got bytes.Buffer
	if _, err := got.ReadFrom(reader); err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if got.String() != want {
		t.Errorf("contents = %q, want %q", got.String(), want)
	}
}

// TestEncryptedArchiveNeedsTheSamePassword: the password lives in the settings
// of the instance doing the work, so restoring elsewhere means setting it there
// first. That is a real constraint and the error has to say so rather than
// reporting a corrupt file.
func TestEncryptedArchiveNeedsTheSamePassword(t *testing.T) {
	source := withBlobs(t, newFixture(t))
	setBackupPassword(t, source, "a good long password")
	mustCreate(t, source, source.now, 3600)

	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	// Nothing recognisable should survive into the file.
	if bytes.Contains(buf.Bytes(), []byte("Acme")) {
		t.Error("the customer name is legible in an encrypted archive")
	}

	// No password set on the target.
	blank := withBlobs(t, newFixture(t))
	_, err := blank.svc.RestoreArchive(blank.ctx, bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err == nil {
		t.Fatal("an encrypted archive restored with no password at all")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("the error should say the archive is encrypted, got: %v", err)
	}

	// The wrong password.
	wrong := withBlobs(t, newFixture(t))
	setBackupPassword(t, wrong, "not the same password")
	if _, err := wrong.svc.RestoreArchive(wrong.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
		t.Fatal("an encrypted archive restored under the wrong password")
	}

	// The right one.
	target := withBlobs(t, newFixture(t))
	setBackupPassword(t, target, "a good long password")
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("restore with the right password: %v", err)
	}
	if result.Entries != 1 {
		t.Errorf("restored %d entries, want 1", result.Entries)
	}
}

// TestRestoreStillReadsABareJSONBackup: every backup taken before archives
// existed is a .json file, and a backup format that abandons its own old files
// is not a backup format.
func TestRestoreStillReadsABareJSONBackup(t *testing.T) {
	source := newFixture(t)
	mustCreate(t, source, source.now, 3600)

	var buf bytes.Buffer
	if err := source.svc.WriteBackup(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	target := newFixture(t)
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Entries != 1 {
		t.Errorf("restored %d entries, want 1", result.Entries)
	}
}

// TestArchiveIsIdempotent: restoring the same archive twice must not double
// anybody's week, nor duplicate their receipts.
func TestArchiveIsIdempotent(t *testing.T) {
	source := withBlobs(t, newFixture(t))
	entry := mustCreate(t, source, source.now, 3600)
	if _, err := source.svc.Attach(source.ctx, "time_entry", entry.ID,
		"note.txt", strings.NewReader("evidence")); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	target := withBlobs(t, newFixture(t))
	for round := 1; round <= 2; round++ {
		if _, err := target.svc.RestoreArchive(target.ctx,
			bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
			t.Fatalf("restore %d: %v", round, err)
		}
	}

	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("after restoring twice there are %d entries, want 1", len(entries))
	}
	attachments, err := target.svc.Attachments(target.ctx, "time_entry", entries[0].ID)
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Errorf("after restoring twice there are %d attachments, want 1", len(attachments))
	}
}

// TestArchiveExplainsItself: the readme is for the person who opens the archive
// in five years with no copy of this application to hand.
func TestArchiveExplainsItself(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)

	var buf bytes.Buffer
	if err := f.svc.WriteArchive(f.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	reader := mustOpenArchive(t, buf.Bytes(), "")
	names := reader.Names()
	for _, want := range []string{ArchiveReadme, ArchiveDocument} {
		if !listHas(names, want) {
			t.Errorf("the archive has no %s; it holds %v", want, names)
		}
	}

	readme, err := reader.ReadFile(ArchiveReadme)
	if err != nil {
		t.Fatalf("read readme: %v", err)
	}
	for _, want := range []string{ArchiveDocument, "1 time entries", "restore"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the readme does not mention %q:\n%s", want, readme)
		}
	}
}

func listHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// setBackupPassword stores an archive password on a fixture's instance.
func setBackupPassword(t *testing.T, f *fixture, password string) {
	t.Helper()
	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", DefaultRate: "0",
		WeekStart: 1, MaxTimerHours: 12, BackupPassword: password,
	}); err != nil {
		t.Fatalf("set backup password: %v", err)
	}
}

// mustOpenArchive opens an archive's bytes for inspection in a test.
func mustOpenArchive(t *testing.T, data []byte, password string) *archive.Reader {
	t.Helper()
	reader, err := archive.NewReader(bytes.NewReader(data), int64(len(data)), password)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	return reader
}

// TestArchivePathsKeepTheirExtension.
//
// The entry name is the content hash, which is what makes it unique and safe.
// The extension is added so that somebody who unzips an archive by hand can
// double-click a receipt instead of guessing what it is - and a guard meant to
// reject path separators once stripped every extension instead, which no test
// noticed because nothing in the application reads the name back.
func TestArchivePathsKeepTheirExtension(t *testing.T) {
	cases := map[string]string{
		"receipt.pdf":       ".pdf",
		"scan.TIFF":         ".tiff",
		"photo.jpeg":        ".jpeg",
		"no-extension":      "",
		"../../etc/passwd":  "",
		"odd.name.with.dot": ".dot",
		"trailing.":         "",
		"weird.p!g":         "",
		"long.extensionish": "",
	}
	for filename, want := range cases {
		got := archivePathFor(domain.Attachment{
			SHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Filename: filename,
		})
		wantPath := ArchiveAttachmentDir +
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + want
		if got != wantPath {
			t.Errorf("archivePathFor(%q) = %q, want %q", filename, got, wantPath)
		}
	}
}
