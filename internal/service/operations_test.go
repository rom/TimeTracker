package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Backups on disk, attachments in the blob store, and the two amounts that are
// computed rather than typed.
//
// All of these touch something outside the database, which is where the
// awkward failures live: a file half written when the process died, a receipt
// whose bytes are gone, a filename that is a path traversal.

// TestABackupOnDiskIsCompleteOrAbsent.
//
// Written to a temporary file and renamed, so a run interrupted half way
// through leaves nothing rather than a truncated archive that looks like a
// backup. What this asserts is the visible end of that: after the call there
// is one file, it is the whole archive, and nothing else is left behind.
func TestABackupOnDiskIsCompleteOrAbsent(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)

	dir := filepath.Join(t.TempDir(), "backups", "nested")

	// The directory is created rather than required: the first backup on a
	// fresh installation must not fail because nobody made a folder.
	file, err := f.svc.WriteBackupFile(f.ctx, dir, BackupOptions{})
	if err != nil {
		t.Fatalf("write a backup: %v", err)
	}
	if file.SizeBytes == 0 {
		t.Error("the backup is empty")
	}
	if !strings.HasSuffix(file.Name, ".zip") {
		t.Errorf("the backup is named %q, want a .zip", file.Name)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the backup directory: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the directory holds %v, want one file and no temporary leftovers", names)
	}

	// Only the owner may read it: a backup is the whole database in one file.
	info, err := os.Stat(file.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the backup's mode is %04o, want it readable only by its owner", mode)
	}

	// And it is a restorable archive rather than a file of the right size.
	handle, err := os.Open(file.Path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = handle.Close() }()
	target := newFixture(t)
	result, err := target.svc.RestoreArchive(target.ctx, handle, info.Size())
	if err != nil {
		t.Fatalf("the written backup is not restorable: %v", err)
	}
	if result.Entries == 0 && result.Skipped == 0 {
		t.Error("the written backup carried no records")
	}
}

// TestListingAndPruningBackups.
//
// The backup screen lists what is on disk and the scheduled run prunes to a
// number somebody chose. Pruning has to keep the newest, since those are the
// ones worth having, and listing a directory that does not exist is an empty
// list rather than an error - which is what a fresh installation looks like.
func TestListingAndPruningBackups(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)

	dir := t.TempDir()

	// Nothing there yet.
	if files, err := f.svc.ListBackupFiles(dir); err != nil || len(files) != 0 {
		t.Errorf("listing an empty directory = %d files, %v", len(files), err)
	}
	missing := filepath.Join(dir, "not-created-yet")
	if files, err := f.svc.ListBackupFiles(missing); err != nil || files != nil {
		t.Errorf("listing a directory that does not exist = %v, %v; want an empty "+
			"list and no error", files, err)
	}

	// Four backups, a minute apart so their names differ and their order is
	// knowable.
	var written []string
	for i := 0; i < 4; i++ {
		file, err := f.svc.WriteBackupFile(f.ctx, dir, BackupOptions{})
		if err != nil {
			t.Fatalf("write backup %d: %v", i, err)
		}
		written = append(written, file.Name)
		f.now = f.now.Add(time.Minute)
	}

	// Something that is not a backup, and a directory, are both ignored rather
	// than listed or deleted.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("write a decoy: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "a-folder"), 0o700); err != nil {
		t.Fatalf("make a decoy directory: %v", err)
	}

	files, err := f.svc.ListBackupFiles(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 4 {
		var names []string
		for _, file := range files {
			names = append(names, file.Name)
		}
		t.Fatalf("listed %v, want only the four archives", names)
	}
	// Newest first, which is the order the screen shows and the order pruning
	// depends on.
	for i := 1; i < len(files); i++ {
		if files[i].CreatedAt.After(files[i-1].CreatedAt) {
			t.Errorf("the listing is not newest-first at %d: %s then %s",
				i, files[i-1].Name, files[i].Name)
		}
	}

	pruned, err := f.svc.PruneBackups(dir, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned %d, want the 2 oldest of 4", pruned)
	}

	after, err := f.svc.ListBackupFiles(dir)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("%d backups remain, want 2", len(after))
	}
	// The two that remain are the newest two, not whichever the filesystem
	// happened to return first.
	kept := map[string]bool{after[0].Name: true, after[1].Name: true}
	for _, name := range written[2:] {
		if !kept[name] {
			t.Errorf("pruning kept %v and threw away the newer %q", kept, name)
		}
	}

	// The decoys survived: pruning deletes backups, not the directory's
	// contents.
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("pruning deleted a file that was not a backup: %v", err)
	}

	// Keeping fewer than one is refused rather than deleting everything.
	if _, err := f.svc.PruneBackups(dir, 0); err == nil {
		if remaining, _ := f.svc.ListBackupFiles(dir); len(remaining) == 0 {
			t.Error("pruning to zero deleted every backup there was")
		}
	}

	// Pruning when there is nothing to prune is a no-op.
	if n, err := f.svc.PruneBackups(dir, 10); err != nil || n != 0 {
		t.Errorf("pruning below the limit = %d, %v; want 0 and no error", n, err)
	}
}

// TestABackupSaysWhatItCovers.
//
// A partial backup that looked like a complete one would be the worst kind of
// backup: somebody would restore it and believe the missing years never
// existed. So the scope travels in the document and reads as prose.
func TestABackupSaysWhatItCovers(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	whole, err := f.svc.CreateBackup(f.ctx, BackupOptions{})
	if err != nil {
		t.Fatalf("whole backup: %v", err)
	}
	if whole.FormatVersion == 0 {
		t.Error("the backup carries no format version, so a future reader cannot " +
			"tell what it is looking at")
	}
	if whole.Application == "" {
		t.Error("the backup does not say what wrote it, which is what matters " +
			"when a restore goes wrong months later")
	}

	narrowed, err := f.svc.CreateBackup(f.ctx, BackupOptions{
		CustomerID: project.CustomerID,
		ProjectID:  project.ID,
		From:       f.now.AddDate(0, 0, -1),
		To:         f.now.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("narrowed backup: %v", err)
	}
	described := strings.ToLower(narrowed.Scope.Description)
	if described == "" {
		t.Fatal("a backup covering one project over two days describes itself as " +
			"nothing, so it is indistinguishable from a complete one")
	}
	// By name rather than by id: the id means nothing to somebody reading a
	// file, and is not even the same number in the instance it is restored
	// into.
	for _, mentioned := range []string{"acme", "migration"} {
		if !strings.Contains(described, mentioned) {
			t.Errorf("the scope reads %q, which does not name the %s it was "+
				"narrowed to", narrowed.Scope.Description, mentioned)
		}
	}
	if strings.Contains(described, "customer 1") || strings.Contains(described, "project 1") {
		t.Errorf("the scope reads %q, which answers \"which backup is this\" "+
			"with a database id", narrowed.Scope.Description)
	}

	// And the last day it covers, not the exclusive bound. f.now is the 16th
	// and To is the 17th, so a backup that said "to 2026-03-17" would claim a
	// day it does not hold.
	if last := f.now.Format("2006-01-02"); !strings.Contains(described, "to "+last) {
		t.Errorf("the scope reads %q, want it to end on %s - the last day it "+
			"actually covers", narrowed.Scope.Description, last)
	}
}

// TestAFilenameIsNeverAPath.
//
// The uploaded name is shown to people and is never used as a path component,
// which is what removes traversal and case-collision problems in one move -
// the file itself is stored under its hash. What is left to get right is the
// name as text: a traversal attempt, control characters, and a name long
// enough to break whatever renders it.
func TestAFilenameIsNeverAPath(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	entry := mustCreate(t, f, f.now, 3600)

	cases := []struct {
		name     string
		filename string
	}{
		{"a traversal", "../../../etc/passwd"},
		{"a windows traversal", `..\..\windows\system32`},
		{"only dots", ".."},
		{"a single dot", "."},
		{"empty", ""},
		{"control characters", "receipt\x00\x01\x1f.txt"},
		{"a newline", "receipt\n.txt"},
		{"very long", strings.Repeat("a", 500) + ".txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attachment, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry,
				entry.ID, tc.filename, strings.NewReader("a receipt"))
			if err != nil {
				// A refusal is fine; what must not happen is storing it as given.
				return
			}
			stored := attachment.Filename
			if stored == "" {
				t.Error("the attachment was stored with no name at all")
			}
			if len(stored) > 200 {
				t.Errorf("the stored name is %d characters long", len(stored))
			}
			for _, r := range stored {
				if r < 32 || r == 127 {
					t.Errorf("the stored name %q still carries a control character", stored)
					break
				}
			}
			for _, fragment := range []string{"..", "/", `\`} {
				if strings.Contains(stored, fragment) {
					t.Errorf("the stored name %q still contains %q", stored, fragment)
				}
			}
		})
	}
}

// TestAReceiptWhoseFileHasGone.
//
// The row says there is a receipt and the blob store does not have it, which
// is what a restore that missed the attachments leaves behind. Opening it has
// to be a plain "not found" rather than a server error, so the screen the
// attachment is listed on keeps working.
func TestAReceiptWhoseFileHasGone(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	entry := mustCreate(t, f, f.now, 3600)

	attachment, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry, entry.ID,
		"receipt.txt", strings.NewReader("a receipt"))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Readable to begin with, so the check below means something.
	if _, reader, err := f.svc.OpenAttachment(f.ctx, attachment.ID); err != nil {
		t.Fatalf("open: %v", err)
	} else {
		_ = reader.Close()
	}

	// Take the bytes away, leaving the row - the state a restore that missed
	// the attachments leaves behind. No service method produces it, so this
	// reaches past the service to the store itself.
	if err := f.blobs.Remove(attachment.SHA256); err != nil {
		t.Fatalf("remove the blob: %v", err)
	}

	if _, _, err := f.svc.OpenAttachment(f.ctx, attachment.ID); err == nil {
		t.Error("an attachment whose file is gone opened anyway")
	}

	// The listing still works: one missing file must not take the screen down.
	listed, err := f.svc.Attachments(f.ctx, domain.AttachmentOwnerTimeEntry, entry.ID)
	if err != nil {
		t.Errorf("listing attachments with one missing file: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("%d attachments listed, want the 1 row that still exists", len(listed))
	}

	// And deleting the row is not blocked by the missing file.
	if err := f.svc.DeleteAttachment(f.ctx, attachment.ID); err != nil {
		t.Errorf("deleting an attachment whose file is gone: %v", err)
	}
}

// TestTheSameFileAttachedTwiceIsStoredOnce.
//
// Blobs are keyed by hash, so two receipts with identical bytes share one
// file. The consequence to get right is deletion: removing one attachment must
// not take the other's bytes with it.
func TestTheSameFileAttachedTwiceIsStoredOnce(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	first := mustCreate(t, f, at(f.now, 9, 0), 3600)
	second := mustCreate(t, f, at(f.now, 11, 0), 3600)

	const content = "the very same receipt"
	a, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry, first.ID,
		"one.txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	b, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerTimeEntry, second.ID,
		"two.txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatalf("identical bytes hashed to %q and %q", a.SHA256, b.SHA256)
	}

	if err := f.svc.DeleteAttachment(f.ctx, a.ID); err != nil {
		t.Fatalf("delete the first: %v", err)
	}

	// The second is still readable, which is the whole point.
	_, reader, err := f.svc.OpenAttachment(f.ctx, b.ID)
	if err != nil {
		t.Fatalf("deleting one attachment took the other's bytes: %v", err)
	}
	_ = reader.Close()

	// Once the last reference goes, a sweep can reclaim the file. Before that
	// it must not.
	if swept, err := f.svc.SweepBlobs(f.ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	} else if swept != 0 {
		t.Errorf("the sweep reclaimed %d files while one was still referenced", swept)
	}

	if err := f.svc.DeleteAttachment(f.ctx, b.ID); err != nil {
		t.Fatalf("delete the second: %v", err)
	}
	if _, err := f.svc.SweepBlobs(f.ctx); err != nil {
		t.Fatalf("sweep after the last reference went: %v", err)
	}
}

// TestACostPricedByQuantity.
//
// Mileage and a per diem are the two amounts somebody does not type: a
// distance at the customer's rate per kilometre, and a number of days at their
// daily allowance. The quantity is held in thousandths so a distance stays
// exact without a float touching a persisted field.
func TestACostPricedByQuantity(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2020-01-01",
		Rules: domain.RateRules{
			MileageRateMinor: 250,    // 2.50 per km
			PerDiemMinor:     30_000, // 300.00 a day
		},
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}

	cases := []struct {
		name     string
		unit     domain.ExpenseUnit
		quantity string
		want     int64
	}{
		{"a whole number of kilometres", domain.UnitKilometre, "40", 40 * 250},
		{"a fractional distance", domain.UnitKilometre, "42.5", 42_500 * 250 / 1000},
		{"a number of days", domain.UnitDay, "3", 3 * 30_000},
		{"half a day", domain.UnitDay, "0.5", 15_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
				ProjectID: project.ID, SpentOn: "2026-03-16",
				Category: "travel", Description: tc.name,
				Unit: tc.unit, Quantity: tc.quantity, Billable: true,
			})
			if err != nil {
				t.Fatalf("record: %v", err)
			}
			if expense.AmountMinor != tc.want {
				t.Errorf("%s came to %d minor units, want %d",
					tc.name, expense.AmountMinor, tc.want)
			}
			if expense.UnitRateMinor == 0 {
				t.Error("the rate it was priced at was not frozen onto the expense, " +
					"so a later rate change would silently restate it")
			}
		})
	}

	// A unit nothing recognises is refused rather than priced at zero.
	if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Unit: domain.ExpenseUnit("furlong"), Quantity: "3",
	}); err == nil {
		t.Error("an expense was priced in furlongs")
	}

	// So is a quantity that is not a number.
	if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Unit: domain.UnitKilometre, Quantity: "about forty",
	}); err == nil {
		t.Error("an expense was priced from a quantity in words")
	}
}

// TestWhichCostsNeedAReceipt.
//
// A contract can require evidence above an amount. The rule has to look at
// what is attached rather than only at the figure, or every expense over the
// threshold would keep asking for a receipt it already has.
func TestWhichCostsNeedAReceipt(t *testing.T) {
	f := withBlobs(t, newFixture(t))

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2020-01-01",
		Rules:         domain.RateRules{ReceiptRequiredAboveMinor: 5_000}, // 50.00
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}

	small, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Bus fare", Amount: "10", Billable: true,
	})
	if err != nil {
		t.Fatalf("record a small expense: %v", err)
	}
	large, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Flight", Amount: "500", Billable: true,
	})
	if err != nil {
		t.Fatalf("record a large expense: %v", err)
	}

	if needs, err := f.svc.NeedsReceipt(f.ctx, small); err != nil {
		t.Fatalf("small: %v", err)
	} else if needs {
		t.Error("an expense under the threshold was asked for a receipt")
	}
	if needs, err := f.svc.NeedsReceipt(f.ctx, large); err != nil {
		t.Fatalf("large: %v", err)
	} else if !needs {
		t.Error("an expense over the threshold was not asked for a receipt")
	}

	// Attaching one answers the question.
	if _, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerExpense, large.ID,
		"flight.txt", strings.NewReader("a boarding pass")); err != nil {
		t.Fatalf("attach: %v", err)
	}
	withReceipt, err := f.svc.Expense(f.ctx, large.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if needs, err := f.svc.NeedsReceipt(f.ctx, withReceipt); err != nil {
		t.Fatalf("after attaching: %v", err)
	} else if needs {
		t.Error("an expense that has a receipt is still being asked for one")
	}
}

// TestADecidedProposalCannotBeDecidedAgain.
//
// Two tabs on the inbox, or a reload after pressing accept. The second
// decision has to be refused by name: accepting something already declined
// would quietly resurrect a claim the person rejected.
func TestADecidedProposalCannotBeDecidedAgain(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)
	theirs := f.asUser(colleague)

	project, err := f.svc.Project(f.ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	end := time.Date(2026, 3, 16, 11, 0, 0, 0, time.UTC)
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID,
		StartedAt:    time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		EndedAt:      &end, OnBehalfOf: colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose time: %v", err)
	}
	expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-16", Category: "travel",
		Description: "Taxi", Amount: "100", OnBehalfOf: colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose an expense: %v", err)
	}

	if _, err := f.svc.AcceptEntry(theirs, entry.ID); err != nil {
		t.Fatalf("accept the entry: %v", err)
	}
	if err := f.svc.RejectExpense(theirs, expense.ID, "I walked"); err != nil {
		t.Fatalf("decline the expense: %v", err)
	}

	if _, err := f.svc.AcceptEntry(theirs, entry.ID); err == nil {
		t.Error("an accepted proposal was accepted again")
	}
	if err := f.svc.RejectEntry(theirs, entry.ID, "changed my mind"); err == nil {
		t.Error("an accepted proposal was then declined")
	}
	err = f.svc.AcceptExpense(theirs, expense.ID)
	if err == nil {
		t.Error("a declined claim was accepted, resurrecting it")
	} else if !strings.Contains(err.Error(), "already") {
		t.Errorf("the refusal reads %q, which does not say it was already decided", err)
	}

	// The length of a decline's reason is capped for a time entry and for a
	// week - both refuse over a thousand characters, since the reason is
	// stored, shown and exported - and is *not* capped for an expense. Pinned
	// as the inconsistency it is, and reported rather than changed here.
	huge := strings.Repeat("x", 2000)

	entryReason, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID,
		StartedAt:    time.Date(2026, 3, 18, 9, 0, 0, 0, time.UTC),
		EndedAt:      timePtr(time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC)),
		OnBehalfOf:   colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose more time: %v", err)
	}
	if err := f.svc.RejectEntry(theirs, entryReason.ID, huge); err == nil {
		t.Error("a two-thousand-character reason was accepted for a time entry")
	}

	another, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: "2026-03-17", Category: "travel",
		Description: "Another taxi", Amount: "100", OnBehalfOf: colleague.ID,
	})
	if err != nil {
		t.Fatalf("propose another: %v", err)
	}
	if err := f.svc.RejectExpense(theirs, another.ID, huge); err != nil {
		t.Logf("declining an expense now caps its reason (%v), matching the time "+
			"entry and the week; this test's note is out of date", err)
	}
}

// TestOvertimeIsNoticedBeforeItIsBilled.
//
// The thresholds drive a prompt and nothing else: the application does not
// decide that somebody worked overtime, it points out that a day or a week has
// passed the figure in the contract and lets them say so. A notice nobody gets
// means time billed at the ordinary rate that the contract says is not.
func TestOvertimeIsNoticedBeforeItIsBilled(t *testing.T) {
	f := newFixture(t)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2020-01-01",
		Rules: domain.RateRules{
			OvertimeDailyThresholdSeconds:  8 * 3600,
			OvertimeWeeklyThresholdSeconds: 40 * 3600,
			OvertimeMultiplierPct:          150,
		},
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}

	// An ordinary day: nothing to say.
	mustCreate(t, f, at(f.now, 9, 0), 6*3600)
	if notices, err := f.svc.OvertimeNotices(f.ctx, f.now); err != nil {
		t.Fatalf("notices: %v", err)
	} else if len(notices) != 0 {
		t.Errorf("a six-hour day produced %d notices: %+v", len(notices), notices)
	}

	// Past the daily threshold.
	mustCreate(t, f, at(f.now, 16, 0), 3*3600)
	daily, err := f.svc.OvertimeNotices(f.ctx, f.now)
	if err != nil {
		t.Fatalf("notices: %v", err)
	}
	if len(daily) == 0 {
		t.Fatal("a nine-hour day against an eight-hour threshold produced no notice")
	}
	if daily[0].CustomerName == "" {
		t.Error("the notice does not say which customer's contract it is about")
	}
	if daily[0].ExcessSeconds() <= 0 {
		t.Errorf("the notice says %ds over, want the hour past the threshold",
			daily[0].ExcessSeconds())
	}

	// And past the weekly one, across several days.
	for day := 1; day <= 4; day++ {
		mustCreate(t, f, at(f.now.AddDate(0, 0, day), 9, 0), 9*3600)
	}
	weekly, err := f.svc.OvertimeNotices(f.ctx, f.now.AddDate(0, 0, 4))
	if err != nil {
		t.Fatalf("weekly notices: %v", err)
	}
	if len(weekly) == 0 {
		t.Fatal("forty-five hours against a forty-hour threshold produced no notice")
	}

	// Ordered, so the same week always reads the same way.
	for i := 1; i < len(weekly); i++ {
		if noticeLess(weekly[i], weekly[i-1]) {
			t.Errorf("the notices are not in order at %d: %+v then %+v",
				i, weekly[i-1], weekly[i])
		}
	}

	// With no threshold agreed there is nothing to notice, however long the day.
	bare := newFixture(t)
	mustCreate(t, bare, at(bare.now, 9, 0), 14*3600)
	if notices, err := bare.svc.OvertimeNotices(bare.ctx, bare.now); err != nil {
		t.Fatalf("notices with no terms: %v", err)
	} else if len(notices) != 0 {
		t.Errorf("a fourteen-hour day produced %d notices with no threshold agreed",
			len(notices))
	}
}
