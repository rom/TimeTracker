package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/archive"
	"github.com/rom/timetracker/internal/auth"
)

// Looking inside a backup before restoring it.
//
// A restore merges, so it is not destructive - but it is not nothing either,
// and the question somebody has in front of a list of archives is "which one is
// this". The filename carries a timestamp and nothing else: not the period it
// covers, not whose work is in it, not whether it was taken of everything or of
// one customer. A partial backup restored in the belief that it was complete
// looks exactly like a complete one afterwards, which is the failure this
// exists to prevent.
//
// So this opens an archive, reads its document, and says what is in it. It
// writes nothing: the restore is a separate action, taken after looking.

// BackupPreview is what an archive holds.
type BackupPreview struct {
	// Name is the file this describes, when it came from the backup directory.
	Name string
	// SizeBytes is the file's size on disk.
	SizeBytes int64
	// CreatedAt is when the backup was taken, from the document rather than
	// from the file's timestamp - a copied file keeps its contents and not its
	// modification time.
	CreatedAt time.Time
	// Application records which version wrote it, which is what matters when a
	// restore goes wrong months later.
	Application string
	// FormatVersion is the document's own version.
	FormatVersion int
	// Encrypted says the archive's entries are encrypted, which is answerable
	// without the password and is worth answering: "wrong password" and
	// "corrupt file" are different things to tell somebody.
	Encrypted bool

	// Scope is what the backup was taken of, as the document states it. A
	// partial backup announces itself here rather than looking like a complete
	// one that lost most of its data.
	Scope BackupScope

	// Counts is how many of each kind of record it carries.
	Counts BackupCounts
	// Period is the span of the time entries it holds, which is the question
	// somebody is usually asking.
	FirstEntry, LastEntry time.Time
	// Customers it covers, named, so "which one is this" has an answer that
	// does not require restoring it.
	Customers []string

	// Problems are what makes this archive less than it appears: attachments it
	// lists and does not carry, a format version from the future. Each is
	// something to know before restoring rather than after.
	Problems []string

	// seconds is the total duration of the entries it holds, exposed through
	// Seconds so a template cannot set it.
	seconds int64
}

// BackupCounts is how many records of each kind an archive holds.
type BackupCounts struct {
	Customers   int
	Projects    int
	Assignments int
	TimeEntries int
	Expenses    int
	Attachments int
	// AttachmentBytes is how much of the archive is files rather than records.
	AttachmentBytes int64
}

// Total is every record in the archive, excluding attachments, which are files
// rather than rows.
func (c BackupCounts) Total() int {
	return c.Customers + c.Projects + c.Assignments + c.TimeEntries + c.Expenses
}

// Any reports whether the archive holds anything at all.
func (p BackupPreview) Any() bool { return p.Counts.Total() > 0 }

// Seconds is the total time the archive's entries hold.
func (p BackupPreview) Seconds() int64 { return p.seconds }

// PreviewBackupFile describes an archive in the backup directory.
//
// By name within the directory rather than by path: the directory is the
// server's, and a caller naming a path would be choosing which file on the
// machine to read.
//
// Administrative, like every other read of the server's backup directory: an
// archive in it was written by whoever runs the instance and holds the catalogue
// whole, unnarrowed by anybody's scope. Unlike the download, which builds a
// fresh archive through the acting user's scope, this reads a file the caller
// does not have - which is what made AuthorizeBackup's old check, "may you view
// time entries", too weak to stand here.
func (s *Service) PreviewBackupFile(ctx context.Context, dir, name string) (BackupPreview, error) {
	if err := s.AuthorizeBackup(ctx); err != nil {
		return BackupPreview{}, err
	}
	// The name is taken as a name and never as a path: joining a caller's
	// string onto a directory is how "../../etc/passwd" becomes a file this
	// process reads out loud.
	if name != filepath.Base(name) || name == "" || name == "." || name == ".." {
		return BackupPreview{}, fmt.Errorf("%w: %q is not a backup file name",
			ErrValidation, name)
	}
	path := filepath.Join(dir, name)

	handle, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return BackupPreview{}, ErrNotFound
		}
		return BackupPreview{}, fmt.Errorf("open backup: %w", err)
	}
	defer func() { _ = handle.Close() }()

	info, err := handle.Stat()
	if err != nil {
		return BackupPreview{}, fmt.Errorf("read backup: %w", err)
	}

	preview, err := s.PreviewArchive(ctx, handle, info.Size())
	if err != nil {
		return BackupPreview{}, err
	}
	preview.Name = name
	preview.SizeBytes = info.Size()
	return preview, nil
}

// PreviewArchive describes an archive from its bytes.
//
// Separate from the file path above so an uploaded archive - one somebody is
// about to restore from their own machine - can be described the same way,
// through the same code, as one sitting in the backup directory.
//
// A signed-in caller and nothing more, which is the same gate RestoreArchive
// applies, and for the same reason: the bytes came from them. Describing a file
// somebody already holds withholds nothing, and a preview stricter than the
// restore it precedes would leave them to merge it unseen.
func (s *Service) PreviewArchive(ctx context.Context, r io.ReaderAt, size int64) (BackupPreview, error) {
	if _, err := auth.MustUser(ctx); err != nil {
		return BackupPreview{}, err
	}
	if _, err := auth.MustUser(ctx); err != nil {
		return BackupPreview{}, err
	}

	// A bare .json backup, which is what every file taken before the archive
	// format looked like. Reading those has to keep working here as it does in
	// the restore.
	if !looksLikeZip(r) {
		document, err := io.ReadAll(io.NewSectionReader(r, 0, size))
		if err != nil {
			return BackupPreview{}, fmt.Errorf("%w: the backup file could not be read: %s",
				ErrValidation, err)
		}
		return describeBackup(document, nil, false)
	}

	password, err := s.backupPassword(ctx)
	if err != nil {
		return BackupPreview{}, err
	}
	reader, err := archive.NewReader(r, size, password)
	if err != nil {
		return BackupPreview{}, fmt.Errorf("%w: this file is not a readable archive: %s",
			ErrValidation, err)
	}

	document, err := reader.ReadFile(ArchiveDocument)
	if err != nil {
		return BackupPreview{}, fmt.Errorf(
			"%w: the archive has no %s in it: %s", ErrValidation, ArchiveDocument, err)
	}

	// The names the archive actually carries, so an attachment listed in the
	// document and absent from the file is reported now rather than during a
	// restore.
	carried := map[string]bool{}
	for _, name := range reader.Names() {
		carried[name] = true
	}
	return describeBackup(document, carried, reader.Encrypted())
}

// describeBackup reads a backup document into a summary.
//
// carried is the set of entry names the archive holds, or nil when there is no
// archive around the document - a bare .json file carries no attachments at
// all, which is itself worth reporting.
func describeBackup(document []byte, carried map[string]bool, encrypted bool) (BackupPreview, error) {
	var backup Backup
	if err := json.Unmarshal(document, &backup); err != nil {
		return BackupPreview{}, fmt.Errorf("%w: the backup file could not be read: %s",
			ErrValidation, err)
	}

	preview := BackupPreview{
		CreatedAt:     backup.CreatedAt,
		Application:   backup.Application,
		FormatVersion: backup.FormatVersion,
		Encrypted:     encrypted,
		Scope:         backup.Scope,
		Counts: BackupCounts{
			Customers:   len(backup.Customers),
			Projects:    len(backup.Projects),
			Assignments: len(backup.Assignments),
			TimeEntries: len(backup.TimeEntries),
			Expenses:    len(backup.Expenses),
		},
	}

	for _, entry := range backup.TimeEntries {
		preview.seconds += entry.Seconds
		if preview.FirstEntry.IsZero() || entry.StartedAt.Before(preview.FirstEntry) {
			preview.FirstEntry = entry.StartedAt
		}
		if entry.StartedAt.After(preview.LastEntry) {
			preview.LastEntry = entry.StartedAt
		}
	}

	seen := map[string]bool{}
	for _, customer := range backup.Customers {
		if !seen[customer.Name] {
			seen[customer.Name] = true
			preview.Customers = append(preview.Customers, customer.Name)
		}
	}
	sort.Strings(preview.Customers)

	// Attachments: how many the document lists, and how many the archive
	// actually holds. The difference is the thing worth knowing - a billed
	// expense whose receipt is not in the file comes back without it.
	missing := 0
	for _, attachment := range backup.Attachments {
		preview.Counts.Attachments++
		preview.Counts.AttachmentBytes += attachment.SizeBytes
		if carried == nil || !carried[attachment.Path] {
			missing++
		}
	}
	if missing > 0 {
		if carried == nil {
			preview.Problems = append(preview.Problems, fmt.Sprintf(
				"this is a plain document rather than an archive, so the %d "+
					"attachments it lists are not in it", missing))
		} else {
			preview.Problems = append(preview.Problems, fmt.Sprintf(
				"%d of the %d attachments it lists are not in the file",
				missing, preview.Counts.Attachments))
		}
	}

	// A document from a later version of the application may hold fields this
	// one will drop on restore, which is worth saying before rather than after.
	if backup.FormatVersion > BackupFormatVersion {
		preview.Problems = append(preview.Problems, fmt.Sprintf(
			"this backup is in format version %d and this version of the "+
				"application understands %d; anything newer will be ignored",
			backup.FormatVersion, BackupFormatVersion))
	}

	return preview, nil
}
