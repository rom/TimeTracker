package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Restoring a backup that is not in perfect condition.
//
// A restore merges rather than replaces, and it has to finish. The alternative
// - stopping at the first record it cannot place - would mean one bad row in a
// year's archive loses the rest of the year, and the person restoring would
// have no way to know what did and did not arrive.
//
// So every problem is collected and reported, and everything that can be
// restored is. These tests build archives that are wrong in each of the ways an
// archive can be wrong, and check that the good records land and the bad ones
// are named.

// damagedBackup is a backup document written by hand, so each field can be
// wrong on purpose. Round-tripped through JSON because that is how a restore
// receives it.
func damagedBackup(t *testing.T, backup Backup) []byte {
	t.Helper()
	backup.FormatVersion = 1
	backup.CreatedAt = time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	backup.Application = "timetracker (test)"
	document, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	return document
}

// TestARestoreFinishesAndNamesWhatItCouldNotPlace.
//
// One archive with one of every problem in it. The point is the combination: a
// restore that gave up on the dangling project would never reach the good
// entry below it, and somebody would be left reconciling two sources by hand.
func TestARestoreFinishesAndNamesWhatItCouldNotPlace(t *testing.T) {
	f := newFixture(t)

	started := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	document := damagedBackup(t, Backup{
		Customers: []domain.Customer{
			{Name: "Globex", Currency: "EUR"},
			// A currency that is not one: the record is refused and named.
			{Name: "Initech", Currency: "not-a-currency"},
		},
		// Projects and assignments name their parents rather than carrying an
		// id, because an archive's ids mean nothing in the instance it is
		// restored into.
		Projects: []domain.Project{
			{CustomerName: "Globex", Name: "Rollout", BillableDefault: true},
			// Names a customer the archive does not carry.
			{CustomerName: "Nowhere Ltd", Name: "Orphan", BillableDefault: true},
		},
		Assignments: []domain.Assignment{
			{CustomerName: "Globex", ProjectName: "Rollout", Name: "Support",
				BillableDefault: true},
		},
		TimeEntries: []BackupEntry{
			// Good: everything it refers to is in the archive.
			{
				Customer: "Globex", Project: "Rollout", Assignment: "Support",
				StartedAt: started, Seconds: 3600, Status: "confirmed",
				TimeZone: "UTC", Billable: true,
			},
			// Refers to a customer the archive does not carry.
			{
				Customer: "Nowhere", Project: "Rollout", Assignment: "Support",
				StartedAt: started.Add(2 * time.Hour), Seconds: 3600,
				Status: "confirmed", TimeZone: "UTC",
			},
			// Refers to things that exist, and is not a valid entry: it ends
			// before it starts.
			{
				Customer: "Globex", Project: "Rollout", Assignment: "Support",
				StartedAt: started.Add(4 * time.Hour),
				EndedAt:   timePtr(started.Add(time.Hour)),
				Seconds:   3600, Status: "confirmed", TimeZone: "UTC",
			},
		},
		Expenses: []BackupExpense{
			{
				Customer: "Globex", Project: "Rollout", SpentOn: "2026-03-16",
				Category: "travel", Description: "Taxi", AmountMinor: 10000,
				Currency: "EUR", Status: "confirmed",
			},
			// A project the archive does not carry.
			{
				Customer: "Globex", Project: "Nothing", SpentOn: "2026-03-16",
				Category: "travel", Description: "Taxi", AmountMinor: 10000,
				Currency: "EUR", Status: "confirmed",
			},
			// Not a valid expense: no date.
			{
				Customer: "Globex", Project: "Rollout", SpentOn: "",
				Category: "travel", Description: "Taxi", AmountMinor: 10000,
				Currency: "EUR", Status: "confirmed",
			},
		},
	})

	result, err := f.svc.Restore(f.ctx, bytes.NewReader(document))
	if err != nil {
		t.Fatalf("the restore gave up rather than reporting: %v", err)
	}

	// The good records arrived.
	if result.Entries != 1 {
		t.Errorf("restored %d entries, want the 1 that was placeable", result.Entries)
	}
	if result.Expenses != 1 {
		t.Errorf("restored %d expenses, want the 1 that was placeable", result.Expenses)
	}
	if result.Customers != 1 {
		t.Errorf("restored %d customers, want the 1 that was valid", result.Customers)
	}

	// And every problem is named, with enough in the text to find the record.
	problems := strings.Join(result.Problems, "\n")
	for _, named := range []string{"Initech", "Orphan", "Nowhere", "Nothing"} {
		if !strings.Contains(problems, named) {
			t.Errorf("the problems do not name %q:\n%s", named, problems)
		}
	}
	if len(result.Problems) < 6 {
		t.Errorf("%d problems reported, want one for each record that could not "+
			"be placed:\n%s", len(result.Problems), problems)
	}

	// The entry that did arrive is readable, which is the real test of a
	// restore: not the count, but that the timesheet works afterwards.
	day, err := f.svc.Day(f.ctx, started)
	if err != nil {
		t.Fatalf("read the day: %v", err)
	}
	if len(day.Entries) != 1 {
		t.Errorf("the restored day holds %d entries, want 1", len(day.Entries))
	}
}

// TestRestoringTheSameArchiveTwiceChangesNothingTheSecondTime.
//
// A merge, not an append. Somebody restoring a weekly archive on top of last
// week's data must not end up with every overlapping day counted twice - and
// the second run has to say so, by counting what it skipped, rather than
// reporting a successful import of nothing.
func TestRestoringTheSameArchiveTwiceChangesNothingTheSecondTime(t *testing.T) {
	f := newFixture(t)

	started := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	document := damagedBackup(t, Backup{
		Customers: []domain.Customer{{Name: "Globex", Currency: "EUR"}},
		Projects: []domain.Project{
			{CustomerName: "Globex", Name: "Rollout", BillableDefault: true},
		},
		Assignments: []domain.Assignment{
			{CustomerName: "Globex", ProjectName: "Rollout", Name: "Support",
				BillableDefault: true},
		},
		TimeEntries: []BackupEntry{{
			Customer: "Globex", Project: "Rollout", Assignment: "Support",
			StartedAt: started, Seconds: 3600, Status: "confirmed",
			TimeZone: "UTC", Billable: true,
		}},
		Expenses: []BackupExpense{{
			Customer: "Globex", Project: "Rollout", SpentOn: "2026-03-16",
			Category: "travel", Description: "Taxi", AmountMinor: 10000,
			Currency: "EUR", Status: "confirmed",
		}},
	})

	first, err := f.svc.Restore(f.ctx, bytes.NewReader(document))
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	if first.Entries != 1 || first.Expenses != 1 {
		t.Fatalf("the first restore brought in %d entries and %d expenses, want 1 each",
			first.Entries, first.Expenses)
	}

	second, err := f.svc.Restore(f.ctx, bytes.NewReader(document))
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if second.Entries != 0 {
		t.Errorf("the second restore brought in %d entries, want none - the day "+
			"would be counted twice", second.Entries)
	}
	if second.Skipped == 0 {
		t.Error("the second restore skipped nothing, so it reported a successful " +
			"import of nothing at all")
	}
	if len(second.Problems) != 0 {
		t.Errorf("restoring a file that overlaps existing data reported problems, "+
			"which is normal and expected rather than a problem:\n%s",
			strings.Join(second.Problems, "\n"))
	}

	// The day still holds one entry, not two.
	day, err := f.svc.Day(f.ctx, started)
	if err != nil {
		t.Fatalf("read the day: %v", err)
	}
	if len(day.Entries) != 1 {
		t.Errorf("the day holds %d entries after restoring the same archive twice, want 1",
			len(day.Entries))
	}
}

// TestAnArchiveListingAttachmentsItDoesNotCarry.
//
// The bytes are not in the JSON document, because base64 inside JSON would
// triple the size of a backup whose bulk is photographs. So a bare .json
// backup can describe its attachments and cannot restore them.
//
// What the code does with that differs by path, and both are pinned here
// because the difference is not visible from either one alone:
//
//   - The archive path reports it, which is what the Attachments field's own
//     comment promises: "a bare .json file lists them and cannot restore them,
//     which the restore reports rather than hides".
//   - The bare-JSON path says nothing at all, because the reporting lives in
//     restoreAttachments and that is only reached when there is an archive to
//     read the files out of.
//
// The second is a gap against the documented intent rather than a danger: the
// records all arrive, and only the receipts are silently absent. Pinned rather
// than fixed, so closing it is a deliberate change.
func TestAnArchiveListingAttachmentsItDoesNotCarry(t *testing.T) {
	f := newFixture(t)

	started := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	document := damagedBackup(t, Backup{
		Customers: []domain.Customer{{Name: "Globex", Currency: "EUR"}},
		Projects: []domain.Project{
			{CustomerName: "Globex", Name: "Rollout", BillableDefault: true},
		},
		Assignments: []domain.Assignment{
			{CustomerName: "Globex", ProjectName: "Rollout", Name: "Support",
				BillableDefault: true},
		},
		TimeEntries: []BackupEntry{{
			Customer: "Globex", Project: "Rollout", Assignment: "Support",
			StartedAt: started, Seconds: 3600, Status: "confirmed",
			TimeZone: "UTC", Billable: true,
		}},
		Attachments: []BackupAttachment{{
			Path:      "attachments/abc.txt",
			OwnerType: domain.AttachmentOwnerTimeEntry,
			OwnerKey:  entryOwnerKey("Globex", "Rollout", "Support", started),
			SHA256:    "abc", Filename: "receipt.txt",
			MIME: "text/plain", SizeBytes: 9,
		}},
	})

	result, err := f.svc.Restore(f.ctx, bytes.NewReader(document))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Entries != 1 {
		t.Errorf("restored %d entries, want 1 - a missing receipt must not cost "+
			"the record it belongs to", result.Entries)
	}
	if result.Attachments != 0 {
		t.Errorf("restored %d attachments from a document that carries none",
			result.Attachments)
	}
	if len(result.Problems) != 0 {
		t.Errorf("the bare-JSON path now reports %v; if that is deliberate, this "+
			"test and the note above it should say so", result.Problems)
	}
}

// TestAnArchiveWhoseAttachmentsHaveNowhereToGo.
//
// A real archive, carrying a receipt, restored into an instance with no
// attachment storage configured. The records come back and the files cannot,
// which has to be said rather than left for somebody to discover when they
// open an expense and find the evidence gone.
func TestAnArchiveWhoseAttachmentsHaveNowhereToGo(t *testing.T) {
	// An instance with a blob store, holding an entry with a receipt on it.
	source := withBlobs(t, newFixture(t))
	entry := mustCreate(t, source, source.now, 3600)
	if _, err := source.svc.Attach(source.ctx, domain.AttachmentOwnerTimeEntry,
		entry.ID, "receipt.txt", strings.NewReader("a receipt")); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	// A fresh instance with no blob store at all.
	target := newFixture(t)
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if result.Entries == 0 {
		t.Error("no entries were restored; the records must come back even when " +
			"their attachments cannot")
	}
	if result.Attachments != 0 {
		t.Errorf("restored %d attachments with no storage configured", result.Attachments)
	}
	problems := strings.Join(result.Problems, "\n")
	if !strings.Contains(problems, "attachment") {
		t.Errorf("the restore did not say the attachments could not be stored:\n%s",
			problems)
	}
}

// TestAnArchiveRestoredIntoAnInstanceThatCanHoldItsAttachments.
//
// The whole point of the format: a receipt is the evidence behind a billed
// expense, and a restore that put the row back and lost the photograph put
// back the wrong half.
func TestAnArchiveRestoredIntoAnInstanceThatCanHoldItsAttachments(t *testing.T) {
	source := withBlobs(t, newFixture(t))
	entry := mustCreate(t, source, source.now, 3600)
	if _, err := source.svc.Attach(source.ctx, domain.AttachmentOwnerTimeEntry,
		entry.ID, "receipt.txt", strings.NewReader("a receipt")); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var buf bytes.Buffer
	if err := source.svc.WriteArchive(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	target := withBlobs(t, newFixture(t))
	result, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Attachments != 1 {
		t.Fatalf("restored %d attachments, want the 1 the archive carries (problems: %v)",
			result.Attachments, result.Problems)
	}
	if len(result.Problems) != 0 {
		t.Errorf("a sound archive reported problems: %v", result.Problems)
	}

	// Restoring the same archive again attaches nothing a second time.
	again, err := target.svc.RestoreArchive(target.ctx,
		bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if again.Attachments != 0 {
		t.Errorf("a second restore attached %d more copies of the same receipt",
			again.Attachments)
	}
}

// TestARestoreRefusesWhatIsNotABackupAtAll.
//
// Somebody picks the wrong file. It is refused by name, and nothing is written
// - a half-applied restore from a file that was never a backup would be the
// worst outcome available.
func TestARestoreRefusesWhatIsNotABackupAtAll(t *testing.T) {
	f := newFixture(t)

	for name, content := range map[string]string{
		"a CSV":                  "date,hours\n2026-03-16,2\n",
		"empty":                  "",
		"truncated JSON":         `{"format_version": 1, "customers": [`,
		"JSON of the wrong sort": `{"format_version": 1, "customers": "not a list"}`,
		"a sentence":             "this is not a backup",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.Restore(f.ctx, strings.NewReader(content)); err == nil {
				t.Errorf("%s was accepted as a backup", name)
			}
		})
	}

	// Nothing was written by any of them: the catalogue is still the
	// fixture's own.
	customers, err := f.svc.Customers(f.ctx, true)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	if len(customers) != 1 {
		t.Errorf("%d customers after the refused restores, want the fixture's 1",
			len(customers))
	}
}

// TestAnArchiveWithNothingInItIsNotAnError.
//
// An empty backup is what a brand-new instance produces, and restoring one has
// to be a no-op rather than a failure - otherwise the first thing somebody
// tries after setting the application up appears to be broken.
func TestAnArchiveWithNothingInItIsNotAnError(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Restore(f.ctx, bytes.NewReader(damagedBackup(t, Backup{})))
	if err != nil {
		t.Fatalf("restoring an empty backup: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("an empty backup restored %d records", result.Total())
	}
	if len(result.Problems) != 0 {
		t.Errorf("an empty backup reported problems: %v", result.Problems)
	}
}
