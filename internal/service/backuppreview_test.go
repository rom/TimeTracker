package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Looking inside a backup before restoring it.
//
// The filename carries a timestamp and nothing else. A backup of one customer
// restored in the belief that it was everything looks exactly like a complete
// one afterwards, which is the failure this exists to prevent - so what it says
// about scope and contents has to be read from the document rather than guessed
// from the file.

// TestABackupSaysWhatIsInIt.
//
// The counts, the period, the customers and the scope: "which one is this",
// answered without restoring it.
func TestABackupSaysWhatIsInIt(t *testing.T) {
	source := withBlobs(t, newFixture(t))

	march := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	source.now = march
	entry := mustCreate(t, source, at(march, 9, 0), 4*3600)
	mustCreate(t, source, at(march.AddDate(0, 0, 5), 9, 0), 2*3600)

	project, err := source.svc.Project(source.ctx, source.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if _, err := source.svc.CreateExpense(source.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-02", Category: "travel",
		Description: "Taxi", Amount: "100", Billable: true,
	}); err != nil {
		t.Fatalf("record an expense: %v", err)
	}
	if _, err := source.svc.Attach(source.ctx, domain.AttachmentOwnerTimeEntry,
		entry.ID, "receipt.txt", strings.NewReader("a receipt")); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	preview, err := source.svc.PreviewArchive(source.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	if preview.Counts.TimeEntries != 2 {
		t.Errorf("the preview says %d entries, want 2", preview.Counts.TimeEntries)
	}
	if preview.Counts.Expenses != 1 {
		t.Errorf("the preview says %d expenses, want 1", preview.Counts.Expenses)
	}
	if preview.Counts.Customers != 1 || preview.Counts.Projects != 1 ||
		preview.Counts.Assignments != 1 {
		t.Errorf("the catalogue counts are %+v, want one of each", preview.Counts)
	}
	if preview.Counts.Attachments != 1 {
		t.Errorf("the preview says %d attachments, want 1", preview.Counts.Attachments)
	}
	if preview.Counts.AttachmentBytes == 0 {
		t.Error("the preview says the attachments take no space at all")
	}
	// The customer, the project, the assignment, the two entries and the
	// expense. Attachments are not in it: a receipt is part of the expense it
	// belongs to rather than a seventh record, and counting it would make the
	// total disagree with what the tables below add up to.
	if preview.Counts.Total() != 6 {
		t.Errorf("the total is %d, want the six records", preview.Counts.Total())
	}
	if !preview.Any() {
		t.Error("an archive holding six records reports itself as empty")
	}

	// The hours, which is what somebody is usually looking for.
	if preview.Seconds() != 6*3600 {
		t.Errorf("the preview says %ds, want the six hours", preview.Seconds())
	}

	// The period, read from the entries rather than from the file's timestamp.
	if preview.FirstEntry.IsZero() || preview.LastEntry.IsZero() {
		t.Error("the preview does not say which period the entries cover")
	}
	if !preview.FirstEntry.Before(preview.LastEntry) {
		t.Errorf("the period runs from %s to %s", preview.FirstEntry, preview.LastEntry)
	}

	// And who it is about.
	if len(preview.Customers) != 1 || preview.Customers[0] != "Acme" {
		t.Errorf("the customers are %v, want the one in the backup", preview.Customers)
	}

	// What wrote it, which is what matters when a restore goes wrong months
	// later.
	if preview.Application == "" {
		t.Error("the preview does not say what wrote the backup")
	}
	if preview.FormatVersion == 0 {
		t.Error("the preview does not say which format the backup is in")
	}
	if preview.CreatedAt.IsZero() {
		t.Error("the preview does not say when the backup was taken")
	}
	if len(preview.Problems) != 0 {
		t.Errorf("a sound archive reported problems: %v", preview.Problems)
	}
}

// TestAPartialBackupAnnouncesItself.
//
// The failure this screen exists to prevent: a backup of one customer over one
// month, restored in the belief that it was everything. Afterwards the two look
// identical, so the scope has to be readable before.
func TestAPartialBackupAnnouncesItself(t *testing.T) {
	f := newFixture(t)
	march := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	f.now = march
	mustCreate(t, f, at(march, 9, 0), 4*3600)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	var whole bytes.Buffer
	if err := f.svc.WriteArchive(f.ctx, &whole, BackupOptions{}); err != nil {
		t.Fatalf("write a whole archive: %v", err)
	}
	var narrowed bytes.Buffer
	if err := f.svc.WriteArchive(f.ctx, &narrowed, BackupOptions{
		CustomerID: project.CustomerID,
		From:       march.AddDate(0, 0, -1), To: march.AddDate(0, 0, 1),
	}); err != nil {
		t.Fatalf("write a narrowed archive: %v", err)
	}

	complete, err := f.svc.PreviewArchive(f.ctx, bytes.NewReader(whole.Bytes()), int64(whole.Len()))
	if err != nil {
		t.Fatalf("preview the whole archive: %v", err)
	}
	if !complete.Scope.Everything {
		t.Errorf("a backup of everything does not say so: %+v", complete.Scope)
	}

	partial, err := f.svc.PreviewArchive(f.ctx, bytes.NewReader(narrowed.Bytes()), int64(narrowed.Len()))
	if err != nil {
		t.Fatalf("preview the narrowed archive: %v", err)
	}
	if partial.Scope.Everything {
		t.Error("a backup of one customer over two days says it covers everything")
	}
	if partial.Scope.Description == "" {
		t.Error("a partial backup describes itself as nothing, so it is " +
			"indistinguishable from a complete one")
	}
	// Named, because "customer 1" does not answer "which backup is this" - and
	// the id is not the same number in the instance it would be restored into.
	if !strings.Contains(partial.Scope.Description, "Acme") {
		t.Errorf("the scope reads %q, which does not name the customer it was "+
			"narrowed to", partial.Scope.Description)
	}
}

// TestAPlainDocumentSaysItCarriesNoAttachments.
//
// A bare .json backup can list its attachments and cannot hold them: the bytes
// are not in the document. Restoring one leaves a billed expense with no
// receipt behind it, which is worth knowing before rather than after.
func TestAPlainDocumentSaysItCarriesNoAttachments(t *testing.T) {
	f := newFixture(t)

	document := damagedBackup(t, Backup{
		Customers: []domain.Customer{{Name: "Globex", Currency: "EUR"}},
		TimeEntries: []BackupEntry{{
			Customer: "Globex", Project: "Rollout", Assignment: "Support",
			StartedAt: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
			Seconds:   3600, Status: "confirmed", TimeZone: "UTC",
		}},
		Attachments: []BackupAttachment{{
			Path: "attachments/abc.txt", OwnerType: domain.AttachmentOwnerTimeEntry,
			SHA256: "abc", Filename: "receipt.txt", MIME: "text/plain", SizeBytes: 9,
		}},
	})

	preview, err := f.svc.PreviewArchive(f.ctx,
		bytes.NewReader(document), int64(len(document)))
	if err != nil {
		t.Fatalf("preview a plain document: %v", err)
	}
	if preview.Encrypted {
		t.Error("a plain document reports itself as encrypted")
	}
	if preview.Counts.TimeEntries != 1 {
		t.Errorf("the preview says %d entries, want 1", preview.Counts.TimeEntries)
	}
	if len(preview.Problems) == 0 {
		t.Fatal("a document listing an attachment it cannot carry reported no problem")
	}
	if !strings.Contains(strings.Join(preview.Problems, " "), "attachment") {
		t.Errorf("the problems do not mention attachments: %v", preview.Problems)
	}
}

// TestAnArchiveMissingAFileItListsSaysSoBeforeRestoring.
//
// The same question asked of a real archive: the document lists a receipt and
// the zip does not hold it. The restore reports this too, and by then the
// records are already in.
func TestAnArchiveMissingAFileItListsSaysSoBeforeRestoring(t *testing.T) {
	original := archiveWithAReceipt(t)
	gutted := rewriteArchive(t, original, func(name string, data []byte) ([]byte, bool) {
		return data, !strings.HasPrefix(name, ArchiveAttachmentDir)
	})

	f := newFixture(t)
	preview, err := f.svc.PreviewArchive(f.ctx,
		bytes.NewReader(gutted), int64(len(gutted)))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Problems) == 0 {
		t.Fatal("an archive missing a file it lists reported no problem")
	}
	if !strings.Contains(strings.Join(preview.Problems, " "), "not in the file") {
		t.Errorf("the problems do not say the file is absent: %v", preview.Problems)
	}

	// A sound archive says nothing, so the warning means something.
	sound, err := f.svc.PreviewArchive(f.ctx,
		bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatalf("preview the sound archive: %v", err)
	}
	if len(sound.Problems) != 0 {
		t.Errorf("a sound archive reported %v", sound.Problems)
	}
	if sound.Counts.Attachments != 1 {
		t.Errorf("the sound archive reports %d attachments, want 1",
			sound.Counts.Attachments)
	}
}

// TestPreviewingWhatIsNotABackup.
//
// Somebody picks the wrong file. It is refused by name rather than described as
// an empty backup, which would read as a successful look at nothing.
func TestPreviewingWhatIsNotABackup(t *testing.T) {
	f := newFixture(t)

	for name, content := range map[string]string{
		"a CSV":           "date,hours\n2026-03-16,2\n",
		"empty":           "",
		"truncated JSON":  `{"format_version": 1, "customers": [`,
		"a sentence":      "this is not a backup",
		"a truncated zip": "PK\x03\x04 and then nothing",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.PreviewArchive(f.ctx,
				strings.NewReader(content), int64(len(content))); err == nil {
				t.Errorf("%s was described as a backup", name)
			}
		})
	}
}

// TestABackupIsNamedRatherThanPathed.
//
// The directory belongs to the server and the name comes from a query string.
// Joining one onto the other without checking is how "../../etc/passwd"
// becomes a file this process reads out loud - so a name that is not a name is
// refused before anything is opened.
func TestABackupIsNamedRatherThanPathed(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)

	dir := t.TempDir()
	written, err := f.svc.WriteBackupFile(f.ctx, dir, BackupOptions{})
	if err != nil {
		t.Fatalf("write a backup: %v", err)
	}

	// A secret somewhere else on the machine, which this must never read.
	elsewhere := filepath.Join(t.TempDir(), "secrets.txt")
	if err := os.WriteFile(elsewhere, []byte("not a backup"), 0o600); err != nil {
		t.Fatalf("write the decoy: %v", err)
	}

	// The real file, by name, works.
	preview, err := f.svc.PreviewBackupFile(f.ctx, dir, written.Name)
	if err != nil {
		t.Fatalf("preview by name: %v", err)
	}
	if preview.Name != written.Name {
		t.Errorf("the preview is of %q, want %q", preview.Name, written.Name)
	}
	if preview.SizeBytes != written.SizeBytes {
		t.Errorf("the preview says %d bytes and the file is %d",
			preview.SizeBytes, written.SizeBytes)
	}

	refused := []string{
		"", ".", "..",
		"../" + filepath.Base(elsewhere),
		"../../etc/passwd",
		filepath.Join("..", "..", "..", "etc", "passwd"),
		elsewhere,
		"sub/dir/backup.zip",
	}
	for _, name := range refused {
		label := name
		if label == "" {
			label = "the empty string"
		}
		t.Run(label, func(t *testing.T) {
			if _, err := f.svc.PreviewBackupFile(f.ctx, dir, name); err == nil {
				t.Errorf("a backup was previewed from %q, which is a path rather "+
					"than a name in the backup directory", name)
			}
		})
	}

	// A name that is a name and is not there is simply not found.
	if _, err := f.svc.PreviewBackupFile(f.ctx, dir, "not-here.zip"); err == nil {
		t.Error("a backup that does not exist was previewed")
	}
}

// TestOnlyAnAdministratorReadsAnArchiveOffTheServer.
//
// The backup directory holds the instance whole: archives written by whoever
// runs it, unnarrowed by anybody's scope. Every other route to backup data
// builds a fresh archive through the acting user's scope, so this is the one
// that could hand somebody a project they are not on - and the gate it needs is
// therefore the administrative one rather than "may view time entries", which a
// member with no memberships passes.
//
// An archive the caller supplies is a different question, and answered
// differently below: they already hold the file.
func TestOnlyAnAdministratorReadsAnArchiveOffTheServer(t *testing.T) {
	f := newStrangerFixture(t)

	dir := t.TempDir()
	stored, err := f.svc.WriteBackupFile(f.ctx, dir, BackupOptions{})
	if err != nil {
		t.Fatalf("take a backup as the administrator: %v", err)
	}

	preview, err := f.svc.PreviewBackupFile(f.stranger, dir, stored.Name)
	if err == nil {
		t.Fatalf("a member on no project read the instance's own archive: %+v",
			preview.Counts)
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("the refusal is %v, want a forbidden error so the screen says "+
			"\"you may not\" rather than \"something went wrong\"", err)
	}
	if preview.Counts.Total() != 0 || preview.Customers != nil {
		t.Errorf("the refusal came with contents anyway: %+v %v",
			preview.Counts, preview.Customers)
	}

	// The administrator who wrote it still reads it.
	if _, err := f.svc.PreviewBackupFile(f.ctx, dir, stored.Name); err != nil {
		t.Errorf("the administrator cannot read their own backup: %v", err)
	}
}

// TestAnArchiveYouHoldIsYoursToLookAt.
//
// The other half of the gate above. PreviewArchive is handed the bytes, so the
// caller has the file already and describing it withholds nothing - which is
// what makes previewing before an upload-restore possible for somebody who is
// not an administrator.
func TestAnArchiveYouHoldIsYoursToLookAt(t *testing.T) {
	f := newStrangerFixture(t)

	var buf bytes.Buffer
	if err := f.svc.WriteArchive(f.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	preview, err := f.svc.PreviewArchive(f.stranger,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("preview an archive the caller holds: %v", err)
	}
	if !preview.Any() {
		t.Error("the preview says the archive is empty")
	}
}

// TestAScopeNamingSomethingTheBackupDoesNotCarry.
//
// Two scopes at once can contradict each other - one customer's id and another
// customer's project - and then the catalogue in the file holds neither. The
// description falls back to the id rather than going blank: "project 7" is a
// poor answer to "which backup is this" and an empty one is worse, because an
// empty description is how a complete backup describes itself.
func TestAScopeNamingSomethingTheBackupDoesNotCarry(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	backup, err := f.svc.CreateBackup(f.ctx, BackupOptions{
		// A customer that is not there, and a project that is - but filtered
		// out, because the project list is narrowed by that same customer.
		CustomerID: project.CustomerID + 1000,
		ProjectID:  project.ID,
	})
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	if backup.Scope.Everything {
		t.Error("a backup narrowed to a customer and a project says it covers everything")
	}
	for _, want := range []string{
		fmt.Sprintf("customer %d", project.CustomerID+1000),
		fmt.Sprintf("project %d", project.ID),
	} {
		if !strings.Contains(backup.Scope.Description, want) {
			t.Errorf("the scope reads %q, want it to fall back to %q for a record "+
				"the file does not carry", backup.Scope.Description, want)
		}
	}
}
