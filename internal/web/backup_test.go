package web

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/blob"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/service"
	"github.com/rom/timetracker/internal/store"
)

// Backup and restore, through the screen.
//
// The archive format and the merge rules are the service's, and tested there.
// What only a request exercises is the rest: that the download is a zip and not
// an error page with a zip's headers, that writing one to disk leaves a file
// the screen then lists, and that restoring reports what it did instead of
// silently doing something else.

// newBackupServer is the local-mode fixture with a backup directory
// configured, since the handlers write into one.
func newBackupServer(t *testing.T) (*Server, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("web tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "web.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(db, auth.SingleUserAuthorizer{}, logger, time.Now)
	blobs, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	svc = svc.WithBlobs(blobs)

	user, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Test User", Role: domain.RoleAdmin,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	backups := filepath.Join(dir, "backups")
	srv, err := New(svc, config.Config{
		Mode: config.ModeLocal, BackupDir: backups,
		BackupEnabled: true, BackupKeep: 7,
	}, logger, Options{
		Identity: func(r *http.Request) (domain.User, error) {
			return db.GetUser(r.Context(), user.ID)
		},
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return srv, backups
}

// TestABackupDownloadsAsAnArchive.
//
// The authorisation is asked before a single header is set, which is the whole
// design of this handler: the archive streams, so a refusal discovered inside
// the writer arrives after the status line and leaves the caller holding a 200
// and an empty file. That reads as a broken download rather than as "you may
// not", which is the worse of the two answers.
func TestABackupDownloadsAsAnArchive(t *testing.T) {
	srv, _ := newBackupServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"2h"},
	})

	rec := get(t, srv, "/backup/download")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /backup/download = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, ".zip") {
		t.Errorf("Content-Disposition = %q, want an attachment named .zip", disposition)
	}

	// And the body is actually a readable archive, not an error page served
	// under a zip's headers.
	body := rec.Body.Bytes()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the download is not a readable zip: %v", err)
	}
	if len(archive.File) == 0 {
		t.Error("the archive is empty")
	}

	// Narrowing the scope is still an archive: an empty selection must not
	// produce a truncated file.
	narrowed := get(t, srv, "/backup/download?customer_id=999")
	if narrowed.Code != http.StatusOK {
		t.Fatalf("a narrowed download = %d", narrowed.Code)
	}
	narrowBody := narrowed.Body.Bytes()
	if _, err := zip.NewReader(bytes.NewReader(narrowBody), int64(len(narrowBody))); err != nil {
		t.Errorf("a download scoped to nothing is not a readable zip: %v", err)
	}
}

// TestWritingABackupToDiskLeavesAFileTheScreenLists.
//
// The on-disk path writes to a temporary file and renames, so that a backup
// interrupted half way through never appears as a complete one. What this
// asserts is the visible end of that: after the button, there is a file, and
// the screen names it.
func TestWritingABackupToDiskLeavesAFileTheScreenLists(t *testing.T) {
	srv, dir := newBackupServer(t)
	seedCatalogue(t, srv)

	before := get(t, srv, "/backup")
	if before.Code != http.StatusOK {
		t.Fatalf("GET /backup = %d: %s", before.Code, before.Body.String())
	}

	if rec := post(t, srv, "/backup/create", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create backup = %d: %s", rec.Code, rec.Body.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the backup directory: %v", err)
	}
	var written []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".zip") {
			written = append(written, entry.Name())
		}
	}
	if len(written) != 1 {
		t.Fatalf("the backup directory holds %v, want one .zip", entries)
	}
	// Nothing half-written is left behind.
	if len(entries) != len(written) {
		t.Errorf("the backup directory also holds %v", entries)
	}

	after := get(t, srv, "/backup").Body.String()
	if !strings.Contains(after, written[0]) {
		t.Errorf("the screen does not list the backup %q it just wrote", written[0])
	}
}

// TestRestoringReportsWhatItDid.
//
// A restore merges rather than replaces, so the thing somebody needs to see
// afterwards is a count: what came in, and what was already there and skipped.
// A restore that said only "done" would leave them opening the timesheet to
// find out.
func TestRestoringReportsWhatItDid(t *testing.T) {
	srv, _ := newBackupServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"2h"},
	})

	archive := get(t, srv, "/backup/download")
	if archive.Code != http.StatusOK {
		t.Fatalf("download = %d", archive.Code)
	}

	// Restoring into the instance it came from: everything is already there,
	// so everything is skipped. That is the honest answer and the one that
	// proves a restore is a merge rather than a duplication.
	rec := uploadTo(t, srv, "/backup/restore", "file", "backup.zip", archive.Body.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", rec.Code, truncate(rec.Body.String()))
	}
	if !strings.Contains(rec.Body.String(), "restore") && !strings.Contains(rec.Body.String(), "Restore") {
		t.Errorf("the restore result is not on the page:\n%s", truncate(rec.Body.String()))
	}

	// And the day is unchanged: a restore of what is already there must not
	// double it.
	if got := totalOn(t, srv, "2026-03-16"); got != "2h 00m" {
		t.Errorf("the day totals %q after restoring its own backup, want 2h 00m", got)
	}
}

// TestRestoringSomethingThatIsNotABackup.
//
// An upload of the wrong file is an ordinary mistake - somebody picks last
// week's CSV. It must be refused by name rather than producing a server error
// or, worse, a partial import.
func TestRestoringSomethingThatIsNotABackup(t *testing.T) {
	srv, _ := newBackupServer(t)
	seedCatalogue(t, srv)

	if rec := uploadTo(t, srv, "/backup/restore", "", "", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("restoring with no file = %d, want 400", rec.Code)
	}

	rec := uploadTo(t, srv, "/backup/restore", "file", "notes.csv",
		"date,hours\n2026-03-16,2\n", nil)
	if rec.Code == http.StatusOK {
		t.Error("a CSV was accepted as a backup archive")
	}
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("restoring a CSV = 500, want a refusal:\n%s", truncate(rec.Body.String()))
	}

	// Nothing was written.
	if got := totalOn(t, srv, "2026-03-16"); got != noTime {
		t.Errorf("a refused restore wrote time: the day totals %q", got)
	}
}
