package service

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// TestBackupRoundTrip: a backup taken from one instance must restore into
// another and produce the same hours.
func TestBackupRoundTrip(t *testing.T) {
	source := newFixture(t)
	mustCreate(t, source, source.now, 3600)
	mustCreate(t, source, source.now.Add(2*time.Hour), 5400)

	var buf bytes.Buffer
	if err := source.svc.WriteBackup(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("the backup is empty")
	}

	// A completely separate instance.
	target := newFixture(t)
	result, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(result.Problems) > 0 {
		t.Fatalf("restore reported problems: %v", result.Problems)
	}
	if result.Entries != 2 {
		t.Errorf("restored %d entries, want 2", result.Entries)
	}

	entries, err := target.svc.Entries(target.ctx, EntryFilter{})
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	totals := target.svc.Totals(entries)
	if totals.SummedSeconds != 9000 {
		t.Errorf("restored total = %d seconds, want 9000", totals.SummedSeconds)
	}
}

// TestRestoreIsIdempotent: restoring the same file twice must not double
// somebody's week. This is the property that makes a mistaken restore safe.
func TestRestoreIsIdempotent(t *testing.T) {
	source := newFixture(t)
	mustCreate(t, source, source.now, 3600)

	var buf bytes.Buffer
	if err := source.svc.WriteBackup(source.ctx, &buf, BackupOptions{}); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	target := newFixture(t)
	first, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	second, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}

	if first.Entries != 1 {
		t.Errorf("first restore created %d entries, want 1", first.Entries)
	}
	if second.Entries != 0 {
		t.Errorf("the second restore duplicated %d entries; it should have skipped them",
			second.Entries)
	}
	if second.Skipped == 0 {
		t.Error("the second restore reported nothing skipped")
	}

	entries, _ := target.svc.Entries(target.ctx, EntryFilter{})
	if len(entries) != 1 {
		t.Errorf("after two restores there are %d entries, want 1", len(entries))
	}
}

// TestPartialBackupByDateRange.
func TestPartialBackupByDateRange(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)                   // in range
	mustCreate(t, f, f.now.AddDate(0, 0, 40), 3600) // outside

	var buf bytes.Buffer
	err := f.svc.WriteBackup(f.ctx, &buf, BackupOptions{
		From: f.now.AddDate(0, 0, -1),
		To:   f.now.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("write backup: %v", err)
	}

	body := buf.String()
	if !strings.Contains(body, `"format_version"`) {
		t.Error("the backup has no format version")
	}
	// A partial backup must announce itself, or it looks like a complete one
	// that lost most of its data.
	if strings.Contains(body, `"everything": true`) {
		t.Error("a date-limited backup claims to be everything")
	}

	target := newFixture(t)
	result, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Entries != 1 {
		t.Errorf("the date-limited backup restored %d entries, want 1", result.Entries)
	}
}

// TestPartialBackupByCustomer.
func TestPartialBackupByCustomer(t *testing.T) {
	f := newFixture(t)

	other, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Other Client", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	otherProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: other.ID, Name: "Other work", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	otherAssignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: otherProject.ID, Name: "Other task", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	mustCreate(t, f, f.now, 3600) // the fixture's customer
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: otherAssignment.ID, StartedAt: f.now, DurationSeconds: 7200, Billable: true,
	}); err != nil {
		t.Fatalf("create entry: %v", err)
	}

	var buf bytes.Buffer
	if err := f.svc.WriteBackup(f.ctx, &buf, BackupOptions{CustomerID: other.ID}); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	target := newFixture(t)
	result, err := target.svc.Restore(target.ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Entries != 1 {
		t.Errorf("the customer-limited backup restored %d entries, want 1", result.Entries)
	}

	entries, _ := target.svc.Entries(target.ctx, EntryFilter{})
	for _, e := range entries {
		if e.CustomerName != "Other Client" {
			t.Errorf("an entry from another customer leaked into the backup: %s", e.CustomerName)
		}
	}
}

// TestRestoreRefusesANewerFormat: silently dropping fields it does not
// understand is the worst thing a restore can do.
func TestRestoreRefusesANewerFormat(t *testing.T) {
	f := newFixture(t)

	future := `{"format_version": 999, "created_at": "2026-01-01T00:00:00Z", "customers": []}`
	_, err := f.svc.Restore(f.ctx, strings.NewReader(future))
	if err == nil {
		t.Fatal("a newer backup format was accepted")
	}
	if !strings.Contains(err.Error(), "newer version") {
		t.Errorf("the error does not explain the problem: %v", err)
	}
}

// TestRestoreRefusesNonsense.
func TestRestoreRefusesNonsense(t *testing.T) {
	f := newFixture(t)

	for _, input := range []string{"", "not json at all", "{}", "[]"} {
		if _, err := f.svc.Restore(f.ctx, strings.NewReader(input)); err == nil {
			t.Errorf("input %q was accepted as a backup", input)
		}
	}
}

// TestBackupFilesOnDisk covers the manual and scheduled backup path.
func TestBackupFilesOnDisk(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, f.now, 3600)
	dir := t.TempDir()

	file, err := f.svc.WriteBackupFile(f.ctx, dir, BackupOptions{})
	if err != nil {
		t.Fatalf("write backup file: %v", err)
	}
	if file.SizeBytes == 0 {
		t.Error("the backup file is empty")
	}

	files, err := f.svc.ListBackupFiles(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 backup, found %d", len(files))
	}

	// Retention: several backups, keeping the newest few.
	for i := 0; i < 4; i++ {
		f.now = f.now.Add(time.Second)
		if _, err := f.svc.WriteBackupFile(f.ctx, dir, BackupOptions{}); err != nil {
			t.Fatalf("write backup %d: %v", i, err)
		}
	}
	removed, err := f.svc.PruneBackups(dir, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 3 {
		t.Errorf("pruned %d backups, want 3", removed)
	}

	// A misconfigured retention must never be read as "delete everything".
	before, _ := f.svc.ListBackupFiles(dir)
	if removed, err := f.svc.PruneBackups(dir, 0); err != nil || removed != 0 {
		t.Errorf("keep=0 removed %d backups; it must remove none", removed)
	}
	after, _ := f.svc.ListBackupFiles(dir)
	if len(after) != len(before) {
		t.Error("keep=0 deleted backups")
	}
}

// TestBackupUnauthenticated.
func TestBackupRequiresAnIdentity(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	if err := f.svc.WriteBackup(context.Background(), &buf, BackupOptions{}); err == nil {
		t.Error("a backup was produced without an identity")
	}
}
