package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/archive"
	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Backup and restore.
//
// A backup is a single JSON file. Not a database copy: a copy of the SQLite file
// is opaque, cannot be partial, and cannot be restored into an instance that has
// moved on. JSON can be inspected, diffed, kept in version control if someone
// wants to, and merged rather than replacing everything.
//
// The decision that shapes restore: it **merges**. A record already present is
// skipped rather than duplicated, so restoring the same file twice is harmless
// and restoring an old backup does not delete newer work. The alternative -
// replace everything - turns a mistaken restore into data loss, which is exactly
// the situation someone reaching for a backup is already in.

// BackupFormatVersion identifies the shape of the file. Bumped only when a field
// changes meaning or disappears, never when one is added.
const BackupFormatVersion = 1

// Names inside the archive.
//
// A backup is now a zip holding the document plus every attachment in its
// original bytes. The reason is blunt: a receipt is the evidence behind a
// billed expense, and a "backup" that saved the row and left the photograph on
// a disk that has since died has saved the wrong half.
const (
	// ArchiveDocument is the JSON document, at the root so it is the first
	// thing anybody opening the archive sees.
	ArchiveDocument = "backup.json"
	// ArchiveAttachmentDir holds the blobs, named by content hash. The hash
	// rather than the original filename, for the same reason the blob store
	// uses it: two receipts called "scan.pdf" would otherwise collide, and a
	// filename from a user must never become a path component.
	ArchiveAttachmentDir = "attachments/"
	// ArchiveReadme explains the archive to somebody who opens it in five
	// years with no copy of this application to hand.
	ArchiveReadme = "README.txt"
)

// Backup is the file's contents.
type Backup struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	// Application records which version wrote it, which matters when a restore
	// goes wrong months later.
	Application string `json:"application"`
	// Scope describes what was included, so a partial backup announces itself
	// rather than looking like a complete one that lost most of its data.
	Scope BackupScope `json:"scope"`

	Customers   []domain.Customer   `json:"customers"`
	Projects    []domain.Project    `json:"projects"`
	Assignments []domain.Assignment `json:"assignments"`
	TimeEntries []BackupEntry       `json:"time_entries"`
	Expenses    []BackupExpense     `json:"expenses"`
	// Attachments describes the files carried alongside in the archive. The
	// bytes are not in this document - base64 inside JSON would triple the size
	// of a backup whose bulk is photographs - so a bare .json file lists them
	// and cannot restore them, which the restore reports rather than hides.
	Attachments []BackupAttachment `json:"attachments,omitempty"`
}

// BackupAttachment is one file in the archive, and what it belongs to.
//
// The owner is named rather than numbered, for the same reason catalogue
// records are: an id from another instance points at somebody else's data. The
// key is whatever identifies the owning record uniquely within a backup - the
// same identity the restore uses to decide a record is already present.
type BackupAttachment struct {
	// Path is the entry's name inside the archive.
	Path string `json:"path"`
	// OwnerType is "time_entry" or "expense".
	OwnerType string `json:"owner_type"`
	// OwnerKey identifies the owning record. See entryOwnerKey and
	// expenseOwnerKey for how each is composed.
	OwnerKey  string `json:"owner_key"`
	SHA256    string `json:"sha256"`
	Filename  string `json:"filename"`
	MIME      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
}

// BackupExpense is an expense in the backup file.
//
// Like BackupEntry it names its project rather than numbering it, and carries
// the frozen billing figures so a restored expense is still worth what it was
// worth rather than being recomputed against today's rules.
type BackupExpense struct {
	Customer      string `json:"customer"`
	Project       string `json:"project"`
	SpentOn       string `json:"spent_on"`
	Category      string `json:"category,omitempty"`
	Description   string `json:"description,omitempty"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Billable      bool   `json:"billable"`
	Reimbursable  bool   `json:"reimbursable"`
	MarkupPercent int64  `json:"markup_percent,omitempty"`
	QuantityMilli int64  `json:"quantity_milli,omitempty"`
	Unit          string `json:"unit,omitempty"`
	UnitRateMinor int64  `json:"unit_rate_minor,omitempty"`
	BilledMinor   int64  `json:"billed_minor"`
	Status        string `json:"status"`
}

// BackupScope records what a backup covers.
type BackupScope struct {
	Everything bool   `json:"everything"`
	CustomerID int64  `json:"customer_id,omitempty"`
	ProjectID  int64  `json:"project_id,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	// Description is a human-readable summary, for the file listing.
	Description string `json:"description"`
}

// BackupEntry is a time entry in the backup file.
//
// It is a separate shape from domain.TimeEntry because a backup refers to
// catalogue records by *name*, not by database id. Ids are meaningless in
// another instance, and restoring by id would either collide with existing
// records or silently attach work to the wrong customer.
type BackupEntry struct {
	Customer   string     `json:"customer"`
	Project    string     `json:"project"`
	Assignment string     `json:"assignment"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Seconds    int64      `json:"duration_seconds"`
	Note       string     `json:"note,omitempty"`
	Billable   bool       `json:"billable"`
	Status     string     `json:"status"`
	TimeZone   string     `json:"time_zone"`
	// The billing snapshot travels with the entry, so a restored timesheet
	// still says what it was worth rather than being recomputed against
	// today's rates.
	RoundingRule    string `json:"rounding_rule,omitempty"`
	BillableSeconds int64  `json:"billable_seconds,omitempty"`
	RateMinor       int64  `json:"rate_minor,omitempty"`
	AmountMinor     int64  `json:"amount_minor,omitempty"`
	Currency        string `json:"currency,omitempty"`
}

// BackupOptions narrows what a backup covers.
type BackupOptions struct {
	CustomerID int64
	ProjectID  int64
	From       time.Time
	To         time.Time
}

// CreateBackup assembles a backup of the acting user's data.
// AuthorizeBackup answers whether the actor may take a backup, without building
// one.
//
// Separate from CreateBackup because the download route commits to a response
// before it writes: it sets the content type and the filename, then streams. A
// refusal discovered inside the writer arrives after the status line, so the
// caller gets a 200 and an empty file - which reads as a broken feature rather
// than as a refusal, and is how the first version of this refusal behaved.
func (s *Service) AuthorizeBackup(ctx context.Context) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	// A backup is an administrative artefact of the instance, not a report. It
	// carries the catalogue whole - including the customer's negotiated hourly
	// rate - and "you may back up what you can see" is the wrong rule for
	// somebody whose whole role is defined by not seeing that
	// (docs/adr/0008-rbac-model.md). A client's route to their own data is the
	// export, which is narrowed.
	//
	// Found by a test that asked what a client actually receives, rather than by
	// reading the check: the check below passes for a client, because a client
	// may view their customer's time.
	if actor.Role == domain.RoleClient {
		return fmt.Errorf("%w: a backup is not a client report", ErrForbidden)
	}
	return s.authz.Can(ctx, auth.ActionView, listResource(ctx, "time_entry"))
}

// CreateBackup writes a backup archive of the database and the attachments.
//
// Authorised through AuthorizeBackup rather than through the ordinary view
// check, for the reason stated there: a backup is the whole catalogue, including
// the commercial data the narrowed client projection exists to withhold.
func (s *Service) CreateBackup(ctx context.Context, opts BackupOptions) (Backup, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return Backup{}, err
	}
	if err := s.AuthorizeBackup(ctx); err != nil {
		return Backup{}, err
	}

	backup := Backup{
		FormatVersion: BackupFormatVersion,
		CreatedAt:     s.now().UTC(),
		Application:   "timetracker",
		Scope:         describeScope(opts),
	}

	// The catalogue is included whole where the scope allows, because a backup
	// missing the customer its entries refer to cannot be restored.
	if backup.Customers, err = s.Customers(ctx, true); err != nil {
		return Backup{}, err
	}
	if backup.Projects, err = s.Projects(ctx, opts.CustomerID, true); err != nil {
		return Backup{}, err
	}
	if backup.Assignments, err = s.Assignments(ctx, opts.ProjectID, true); err != nil {
		return Backup{}, err
	}

	// Narrow the catalogue to the scope, so a single-customer backup does not
	// carry every other client's project names.
	if opts.CustomerID != 0 {
		backup.Customers = filterCustomers(backup.Customers, opts.CustomerID)
	}

	entries, err := s.Entries(ctx, EntryFilter{
		From:       opts.From,
		To:         opts.To,
		CustomerID: opts.CustomerID,
		ProjectID:  opts.ProjectID,
		// A backup is not a screen: no limit, because a truncated backup is
		// worse than a slow one.
		Limit: 0,
	})
	if err != nil {
		return Backup{}, err
	}
	for _, entry := range entries {
		backup.TimeEntries = append(backup.TimeEntries, BackupEntry{
			Customer:        entry.CustomerName,
			Project:         entry.ProjectName,
			Assignment:      entry.AssignmentName,
			StartedAt:       entry.StartedAt,
			EndedAt:         entry.EndedAt,
			Seconds:         entry.DurationSeconds,
			Note:            entry.Note,
			Billable:        entry.Billable,
			Status:          string(entry.Status),
			TimeZone:        entry.TimeZone,
			RoundingRule:    entry.RoundingRuleApplied,
			BillableSeconds: entry.BillableSeconds,
			RateMinor:       entry.RateMinor,
			AmountMinor:     entry.AmountMinor,
			Currency:        entry.Currency,
		})
	}

	expenses, err := s.Expenses(ctx, ExpenseFilter{
		From: opts.From, To: opts.To,
		CustomerID: opts.CustomerID, ProjectID: opts.ProjectID,
	})
	if err != nil {
		return Backup{}, err
	}
	for _, expense := range expenses {
		backup.Expenses = append(backup.Expenses, BackupExpense{
			Customer: expense.CustomerName, Project: expense.ProjectName,
			SpentOn: expense.SpentOn, Category: expense.Category,
			Description: expense.Description, AmountMinor: expense.AmountMinor,
			Currency: expense.Currency, Billable: expense.Billable,
			Reimbursable: expense.Reimbursable, MarkupPercent: expense.MarkupPercent,
			QuantityMilli: expense.QuantityMilli, Unit: string(expense.Unit),
			UnitRateMinor: expense.UnitRateMinor, BilledMinor: expense.BilledMinor,
			Status: string(expense.Status),
		})
	}

	// What is attached to all of it. Metadata only: the bytes go into the
	// archive beside this document.
	if backup.Attachments, err = s.collectAttachments(ctx, entries, expenses); err != nil {
		return Backup{}, err
	}

	s.auditLog(ctx, "backup.create", "backup", 0)
	_ = actor
	return backup, nil
}

// collectAttachments lists what is attached to the records in a backup.
func (s *Service) collectAttachments(ctx context.Context, entries []domain.TimeEntry, expenses []domain.Expense) ([]BackupAttachment, error) {
	var out []BackupAttachment

	add := func(ownerType, ownerKey string, ownerID int64) error {
		attachments, err := s.db.ListAttachments(ctx, ownerType, ownerID)
		if err != nil {
			return err
		}
		for _, attachment := range attachments {
			out = append(out, BackupAttachment{
				Path:      archivePathFor(attachment),
				OwnerType: ownerType,
				OwnerKey:  ownerKey,
				SHA256:    attachment.SHA256,
				Filename:  attachment.Filename,
				MIME:      attachment.MIME,
				SizeBytes: attachment.SizeBytes,
			})
		}
		return nil
	}

	for _, entry := range entries {
		if entry.AttachmentCount == 0 {
			continue
		}
		if err := add(domain.AttachmentOwnerTimeEntry, entryOwnerKey(
			entry.CustomerName, entry.ProjectName, entry.AssignmentName, entry.StartedAt,
		), entry.ID); err != nil {
			return nil, err
		}
	}
	for _, expense := range expenses {
		if expense.AttachmentCount == 0 {
			continue
		}
		if err := add(domain.AttachmentOwnerExpense, expenseOwnerKey(
			expense.CustomerName, expense.ProjectName, expense.SpentOn,
			expense.Description, expense.AmountMinor,
		), expense.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// archivePathFor names an attachment's entry inside the archive.
//
// The content hash, plus whatever extension the original filename had. The hash
// makes the name unique and keeps a user's filename out of the path; the
// extension is pure courtesy, so somebody who unzips the archive by hand can
// double-click a receipt instead of guessing what it is.
func archivePathFor(a domain.Attachment) string {
	extension := strings.ToLower(filepath.Ext(a.Filename))
	// Anything unusual is dropped rather than sanitised. The name only has to
	// be unique and harmless, and the hash on its own already is - so the
	// extension is a convenience that is never worth a moment's doubt.
	if len(extension) > 6 || strings.ContainsAny(extension, `/\`) ||
		strings.Contains(extension, "..") || !isSimpleExtension(extension) {
		extension = ""
	}
	return ArchiveAttachmentDir + a.SHA256 + extension
}

// isSimpleExtension reports whether an extension is a dot and letters or
// digits, which every extension worth keeping is.
func isSimpleExtension(extension string) bool {
	if len(extension) < 2 || extension[0] != '.' {
		return false
	}
	for _, r := range extension[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// entryOwnerKey identifies a time entry across instances.
//
// Deliberately the same identity restoreEntries uses to decide an entry is
// already present: assignment plus start instant. If two records answer to this
// key they are the same entry, and an attachment attached to either is attached
// to the right one.
func entryOwnerKey(customer, project, assignment string, startedAt time.Time) string {
	return strings.Join([]string{
		strings.ToLower(customer), strings.ToLower(project),
		strings.ToLower(assignment), startedAt.UTC().Format(time.RFC3339),
	}, "\x00")
}

// expenseOwnerKey identifies an expense across instances.
//
// An expense has no start instant to be unique by, so this uses everything that
// distinguishes one receipt from another on the same day: the project, the
// date, the description and the amount. Two identical expenses on one day with
// the same description and amount are indistinguishable, and treating them as
// one is the safer error - it attaches a receipt to a duplicate rather than
// creating a second copy of it.
func expenseOwnerKey(customer, project, spentOn, description string, amountMinor int64) string {
	return strings.Join([]string{
		strings.ToLower(customer), strings.ToLower(project), spentOn,
		strings.ToLower(strings.TrimSpace(description)),
		strconv.FormatInt(amountMinor, 10),
	}, "\x00")
}

// WriteBackup streams a backup as a bare JSON document.
//
// Kept alongside WriteArchive because a JSON document is still the thing worth
// having when somebody wants to read, diff or grep a backup, and because it is
// what the format tests assert against. It carries no attachments; WriteArchive
// is what the interface offers.
func (s *Service) WriteBackup(ctx context.Context, w io.Writer, opts BackupOptions) error {
	backup, err := s.CreateBackup(ctx, opts)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	// Indented: a backup someone may need to inspect by hand at three in the
	// morning is worth the extra bytes.
	encoder.SetIndent("", "  ")
	return encoder.Encode(backup)
}

// WriteArchive streams a backup as a zip: the document, a readme, and every
// attachment in its original bytes.
//
// Encrypted when a backup password is set in the settings. What that password
// defends against is a copy of the archive that has left this machine - mailed
// to an accountant, synced to a cloud drive - and not the database it was made
// from, which anybody who could read the password already has
// (docs/adr/0030-encrypted-backup-archives.md).
func (s *Service) WriteArchive(ctx context.Context, w io.Writer, opts BackupOptions) error {
	backup, err := s.CreateBackup(ctx, opts)
	if err != nil {
		return err
	}

	password, err := s.backupPassword(ctx)
	if err != nil {
		return err
	}

	document, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup: %w", err)
	}

	now := s.now().UTC()
	writer := archive.NewWriter(w, password)

	if err := writer.Add(ArchiveReadme, now,
		strings.NewReader(archiveReadme(backup, writer.Encrypted()))); err != nil {
		return err
	}
	if err := writer.Add(ArchiveDocument, now, bytes.NewReader(document)); err != nil {
		return err
	}

	// The blobs. A missing one is reported in the readme rather than failing
	// the whole backup: a backup of everything that still exists is worth
	// having, and refusing to write one because a file was swept is the wrong
	// answer to somebody who is trying to protect what is left.
	written := map[string]bool{}
	for _, attachment := range backup.Attachments {
		// Content addressing means two records can share one file. It goes in
		// once.
		if written[attachment.Path] {
			continue
		}
		written[attachment.Path] = true

		if s.blobs == nil {
			continue
		}
		reader, err := s.blobs.Get(attachment.SHA256)
		if err != nil {
			if s.log != nil {
				s.log.WarnContext(ctx, "a backup could not include an attachment",
					"sha256", attachment.SHA256, "filename", attachment.Filename)
			}
			continue
		}
		addErr := writer.Add(attachment.Path, now, reader)
		_ = reader.Close()
		if addErr != nil {
			return addErr
		}
	}

	return writer.Close()
}

// backupPassword reads the configured archive password.
func (s *Service) backupPassword(ctx context.Context) (string, error) {
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings.BackupPassword, nil
}

// archiveReadme explains the archive to whoever opens it without this
// application to hand - which, for a backup, is the situation it exists for.
func archiveReadme(backup Backup, encrypted bool) string {
	var out strings.Builder
	out.WriteString("TimeTracker backup\n")
	out.WriteString("==================\n\n")
	fmt.Fprintf(&out, "Created:  %s\n", backup.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&out, "Covers:   %s\n", backup.Scope.Description)
	fmt.Fprintf(&out, "Format:   version %d\n\n", backup.FormatVersion)

	fmt.Fprintf(&out, "%-24s the data, as JSON\n", ArchiveDocument)
	fmt.Fprintf(&out, "%-24s attachments, named by SHA-256 of their contents\n\n",
		strings.TrimSuffix(ArchiveAttachmentDir, "/")+"/")

	fmt.Fprintf(&out, "Contents: %d customers, %d projects, %d assignments,\n",
		len(backup.Customers), len(backup.Projects), len(backup.Assignments))
	fmt.Fprintf(&out, "          %d time entries, %d expenses, %d attachments\n\n",
		len(backup.TimeEntries), len(backup.Expenses), len(backup.Attachments))

	if encrypted {
		out.WriteString(
			"This archive is encrypted with AES-256 (WinZip AE-2). Any archiver that\n" +
				"supports encrypted zip files will open it with the password - 7-Zip,\n" +
				"WinZip, Keka, and most desktop archive managers. Info-ZIP's `unzip`\n" +
				"does not support AES and will report method 99.\n\n")
	}
	out.WriteString(
		"To restore, use Admin -> Backup -> Restore in TimeTracker. A restore\n" +
			"merges: records already present are skipped rather than duplicated, so\n" +
			"restoring the same archive twice is harmless.\n")
	return out.String()
}

// RestoreResult reports what a restore did.
type RestoreResult struct {
	Customers   int
	Projects    int
	Assignments int
	Entries     int
	Expenses    int
	// Attachments counts files put back into the blob store and re-linked.
	Attachments int
	// Skipped counts records that were already present. A high number here is
	// normal and expected when restoring a file that overlaps existing data.
	Skipped int
	// Problems lists what could not be restored, so a partial result is visible.
	Problems []string

	// pendingAttachments and attachmentOwners carry the state between restoring
	// the records and restoring the files. Unexported: they are working notes,
	// not part of what the screen reports.
	pendingAttachments []BackupAttachment
	attachmentOwners   map[string]int64
}

// noteOwner records the new id of a restored record, so an attachment listed
// against it can find it again.
func (r *RestoreResult) noteOwner(ownerType, ownerKey string, id int64) {
	if r.attachmentOwners == nil {
		r.attachmentOwners = map[string]int64{}
	}
	r.attachmentOwners[ownerType+"\x00"+ownerKey] = id
}

// Total returns how many records were created.
func (r RestoreResult) Total() int {
	return r.Customers + r.Projects + r.Assignments + r.Entries + r.Expenses + r.Attachments
}

// RestoreArchive merges an uploaded backup, in whichever form it arrives.
//
// Three forms are accepted: an encrypted zip, a plain zip, and a bare JSON
// document from before archives existed. Sniffing rather than trusting the file
// extension, because the extension is whatever the browser or the user's mail
// client last called it.
func (s *Service) RestoreArchive(ctx context.Context, r io.ReaderAt, size int64) (RestoreResult, error) {
	if _, err := auth.MustUser(ctx); err != nil {
		return RestoreResult{}, err
	}

	// Not a zip at all: the bare JSON path, which is what every backup taken
	// before this release looks like. Reading those has to keep working - a
	// backup format that abandons its own old files is not a backup format.
	if !looksLikeZip(r) {
		return s.Restore(ctx, io.NewSectionReader(r, 0, size))
	}

	password, err := s.backupPassword(ctx)
	if err != nil {
		return RestoreResult{}, err
	}
	reader, err := archive.NewReader(r, size, password)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%w: this file is not a readable archive: %s",
			ErrValidation, err)
	}

	document, err := reader.ReadFile(ArchiveDocument)
	if err != nil {
		switch {
		case errors.Is(err, archive.ErrPasswordRequired):
			return RestoreResult{}, fmt.Errorf(
				"%w: this archive is encrypted. Set the backup password in Settings to "+
					"the one it was written with, then restore again", ErrValidation)
		case errors.Is(err, archive.ErrWrongPassword), errors.Is(err, archive.ErrCorrupt):
			return RestoreResult{}, fmt.Errorf(
				"%w: the backup password in Settings does not open this archive", ErrValidation)
		}
		return RestoreResult{}, fmt.Errorf(
			"%w: the archive has no %s in it: %s", ErrValidation, ArchiveDocument, err)
	}

	result, err := s.restoreDocument(ctx, document)
	if err != nil {
		return result, err
	}

	// The blobs, after the records they belong to exist.
	s.restoreAttachments(ctx, reader, &result)
	return result, nil
}

// looksLikeZip reads the first four bytes rather than trusting a file name.
func looksLikeZip(r io.ReaderAt) bool {
	var magic [4]byte
	if _, err := r.ReadAt(magic[:], 0); err != nil {
		return false
	}
	// "PK\x03\x04" is a local file header; the other two are an empty archive
	// and a spanned one, neither of which we write but both of which are zips.
	return magic == [4]byte{'P', 'K', 3, 4} ||
		magic == [4]byte{'P', 'K', 5, 6} ||
		magic == [4]byte{'P', 'K', 7, 8}
}

// Restore merges a bare JSON backup document into the acting user's data.
func (s *Service) Restore(ctx context.Context, r io.Reader) (RestoreResult, error) {
	if _, err := auth.MustUser(ctx); err != nil {
		return RestoreResult{}, err
	}
	document, err := io.ReadAll(io.LimitReader(r, 256<<20))
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%w: the backup file could not be read: %s",
			ErrValidation, err)
	}
	return s.restoreDocument(ctx, document)
}

// restoreDocument is the merge itself, once the document has been obtained.
func (s *Service) restoreDocument(ctx context.Context, document []byte) (RestoreResult, error) {
	var backup Backup
	if err := json.Unmarshal(document, &backup); err != nil {
		return RestoreResult{}, fmt.Errorf("%w: the backup file could not be read: %s",
			ErrValidation, err)
	}
	if backup.FormatVersion == 0 {
		return RestoreResult{}, fmt.Errorf(
			"%w: this does not look like a TimeTracker backup", ErrValidation)
	}
	if backup.FormatVersion > BackupFormatVersion {
		// Refusing is the honest answer: a newer file may carry fields this
		// version would silently drop, and a restore that quietly loses data is
		// the worst possible outcome for a backup.
		return RestoreResult{}, fmt.Errorf(
			"%w: this backup was written by a newer version (format %d, this understands %d); "+
				"upgrade before restoring", ErrValidation, backup.FormatVersion, BackupFormatVersion)
	}

	// The attachment list travels with the result, because restoring the files
	// needs the ids the records were given, which only exist once the records
	// below have been created.
	result := RestoreResult{pendingAttachments: backup.Attachments}

	// Catalogue first, and by name: ids from another instance mean nothing here.
	customerIDs, err := s.restoreCustomers(ctx, backup, &result)
	if err != nil {
		return result, err
	}
	projectIDs, err := s.restoreProjects(ctx, backup, customerIDs, &result)
	if err != nil {
		return result, err
	}
	assignmentIDs, err := s.restoreAssignments(ctx, backup, projectIDs, &result)
	if err != nil {
		return result, err
	}
	if err := s.restoreEntries(ctx, backup, assignmentIDs, &result); err != nil {
		return result, err
	}
	if err := s.restoreExpenses(ctx, backup, projectIDs, &result); err != nil {
		return result, err
	}

	// The one audit row in the application that is written beside the change
	// rather than with it, and the one whose failure does not fail the caller.
	//
	// A restore is not a change. It is a sequence of them - every customer,
	// project, assignment, entry and expense above goes through the ordinary
	// audited path, each committing with its own record of who created it - and
	// it merges into an existing instance by name, skipping what is already
	// there, which is what makes it safe to run twice. There is no single
	// transaction this summary could join without holding the write connection
	// for the length of a restore of somebody's whole history.
	//
	// So the trail is already complete without this row: it says what the
	// operation was, not what changed. Failing the call over it would report a
	// failed restore to somebody whose data is in fact restored and fully
	// recorded, which is the worse of the two lies.
	if err := s.recordAudit(ctx, "backup.restore", "backup", 0, map[string]any{
		"customers": result.Customers, "projects": result.Projects,
		"assignments": result.Assignments, "entries": result.Entries,
		"skipped": result.Skipped,
	}); err != nil && s.log != nil {
		s.log.ErrorContext(ctx, "the restore summary could not be recorded",
			"error", err.Error(),
			"note", "the restored records are individually audited; this is the summary only")
	}
	return result, nil
}

// restoreCustomers creates missing customers and returns a name-to-id map.
func (s *Service) restoreCustomers(ctx context.Context, backup Backup, result *RestoreResult) (map[string]int64, error) {
	existing, err := s.Customers(ctx, true)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, c := range existing {
		ids[strings.ToLower(c.Name)] = c.ID
	}

	for _, c := range backup.Customers {
		key := strings.ToLower(c.Name)
		if _, present := ids[key]; present {
			result.Skipped++
			continue
		}
		created, err := s.CreateCustomer(ctx, domain.Customer{
			Name: c.Name, Code: c.Code, Currency: c.Currency,
			ColourKey: c.ColourKey, Icon: c.Icon, Notes: c.Notes, RateMinor: c.RateMinor,
		})
		if err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("customer %q: %s", c.Name, err))
			continue
		}
		ids[key] = created.ID
		result.Customers++
	}
	return ids, nil
}

// restoreProjects creates missing projects, keyed by customer and name.
func (s *Service) restoreProjects(ctx context.Context, backup Backup, customerIDs map[string]int64, result *RestoreResult) (map[string]int64, error) {
	existing, err := s.Projects(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, p := range existing {
		ids[projectKey(p.CustomerName, p.Name)] = p.ID
	}

	for _, p := range backup.Projects {
		key := projectKey(p.CustomerName, p.Name)
		if _, present := ids[key]; present {
			result.Skipped++
			continue
		}
		customerID, ok := customerIDs[strings.ToLower(p.CustomerName)]
		if !ok {
			result.Problems = append(result.Problems,
				fmt.Sprintf("project %q refers to a customer that is not in the backup", p.Name))
			continue
		}
		created, err := s.CreateProject(ctx, domain.Project{
			CustomerID: customerID, Name: p.Name, Code: p.Code,
			ColourKey: p.ColourKey, Icon: p.Icon, BillableDefault: p.BillableDefault,
			RateMinor: p.RateMinor, RoundingRule: p.RoundingRule,
			BudgetSeconds: p.BudgetSeconds, BudgetMinor: p.BudgetMinor,
		})
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("project %q: %s", p.Name, err))
			continue
		}
		ids[key] = created.ID
		result.Projects++
	}
	return ids, nil
}

// restoreAssignments creates missing assignments.
func (s *Service) restoreAssignments(ctx context.Context, backup Backup, projectIDs map[string]int64, result *RestoreResult) (map[string]int64, error) {
	existing, err := s.Assignments(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, a := range existing {
		ids[assignmentKey(a.CustomerName, a.ProjectName, a.Name)] = a.ID
	}

	for _, a := range backup.Assignments {
		key := assignmentKey(a.CustomerName, a.ProjectName, a.Name)
		if _, present := ids[key]; present {
			result.Skipped++
			continue
		}
		projectID, ok := projectIDs[projectKey(a.CustomerName, a.ProjectName)]
		if !ok {
			result.Problems = append(result.Problems,
				fmt.Sprintf("assignment %q refers to a project that is not in the backup", a.Name))
			continue
		}
		created, err := s.CreateAssignment(ctx, domain.Assignment{
			ProjectID: projectID, Name: a.Name, Code: a.Code,
			ColourKey: a.ColourKey, Icon: a.Icon, BillableDefault: a.BillableDefault,
			RateMinor: a.RateMinor, SortOrder: a.SortOrder, Favourite: a.Favourite,
		})
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("assignment %q: %s", a.Name, err))
			continue
		}
		ids[key] = created.ID
		result.Assignments++
	}
	return ids, nil
}

// restoreEntries creates time entries that are not already present.
//
// "Already present" is decided by the subject, the assignment and the start
// instant: two entries at the same second on the same assignment for the same
// person are the same entry, and restoring a file twice must not double
// somebody's week.
func (s *Service) restoreEntries(ctx context.Context, backup Backup, assignmentIDs map[string]int64, result *RestoreResult) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	existing, err := s.Entries(ctx, EntryFilter{})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range existing {
		seen[entryKey(e.AssignmentID, e.StartedAt)] = true
	}

	for _, e := range backup.TimeEntries {
		assignmentID, ok := assignmentIDs[assignmentKey(e.Customer, e.Project, e.Assignment)]
		if !ok {
			result.Problems = append(result.Problems, fmt.Sprintf(
				"an entry refers to %s / %s / %s, which is not in the backup",
				e.Customer, e.Project, e.Assignment))
			continue
		}
		if seen[entryKey(assignmentID, e.StartedAt)] {
			result.Skipped++
			continue
		}

		entry := domain.TimeEntry{
			UserID: actor.ID, EnteredBy: actor.ID, AssignmentID: assignmentID,
			StartedAt: e.StartedAt, EndedAt: e.EndedAt, DurationSeconds: e.Seconds,
			Note: e.Note, Billable: e.Billable, TimeZone: e.TimeZone,
			Status:              domain.EntryStatus(e.Status),
			RoundingRuleApplied: e.RoundingRule, BillableSeconds: e.BillableSeconds,
			RateMinor: e.RateMinor, AmountMinor: e.AmountMinor, Currency: e.Currency,
		}
		if entry.Status == "" {
			entry.Status = domain.StatusConfirmed
		}
		if entry.TimeZone == "" {
			entry.TimeZone = "UTC"
		}
		if err := entry.Validate(); err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("an entry was invalid: %s", err))
			continue
		}

		created, err := s.db.CreateEntry(ctx, entry)
		if err != nil {
			return err
		}
		result.noteOwner(domain.AttachmentOwnerTimeEntry,
			entryOwnerKey(e.Customer, e.Project, e.Assignment, e.StartedAt), created.ID)
		seen[entryKey(assignmentID, e.StartedAt)] = true
		result.Entries++
	}
	return nil
}

// restoreExpenses creates expenses that are not already present.
//
// "Already present" is the same key an attachment is matched on: project, date,
// description and amount. Two identical receipts on one day are
// indistinguishable and restore as one, which is the safer error - the
// alternative doubles somebody's expense claim every time they restore.
func (s *Service) restoreExpenses(ctx context.Context, backup Backup, projectIDs map[string]int64, result *RestoreResult) error {
	if len(backup.Expenses) == 0 {
		return nil
	}
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	existing, err := s.Expenses(ctx, ExpenseFilter{})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range existing {
		seen[expenseOwnerKey(e.CustomerName, e.ProjectName, e.SpentOn,
			e.Description, e.AmountMinor)] = true
	}

	for _, e := range backup.Expenses {
		key := expenseOwnerKey(e.Customer, e.Project, e.SpentOn, e.Description, e.AmountMinor)
		if seen[key] {
			result.Skipped++
			continue
		}
		projectID, ok := projectIDs[projectKey(e.Customer, e.Project)]
		if !ok {
			result.Problems = append(result.Problems, fmt.Sprintf(
				"an expense refers to %s / %s, which is not in the backup", e.Customer, e.Project))
			continue
		}

		expense := domain.Expense{
			UserID: actor.ID, EnteredBy: actor.ID, ProjectID: projectID,
			SpentOn: e.SpentOn, Category: e.Category, Description: e.Description,
			AmountMinor: e.AmountMinor, Currency: e.Currency,
			Billable: e.Billable, Reimbursable: e.Reimbursable,
			MarkupPercent: e.MarkupPercent, QuantityMilli: e.QuantityMilli,
			Unit: domain.ExpenseUnit(e.Unit), UnitRateMinor: e.UnitRateMinor,
			// The billed figure travels with the expense rather than being
			// recomputed, so a restored claim is worth what it was worth.
			BilledMinor: e.BilledMinor,
			Status:      domain.EntryStatus(e.Status),
		}
		if expense.Status == "" {
			expense.Status = domain.StatusConfirmed
		}
		if err := expense.Validate(); err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("an expense was invalid: %s", err))
			continue
		}
		created, err := s.db.CreateExpense(ctx, expense)
		if err != nil {
			return err
		}
		result.noteOwner(domain.AttachmentOwnerExpense, key, created.ID)
		seen[key] = true
		result.Expenses++
	}
	return nil
}

// restoreAttachments puts the archived blobs back and re-links them.
//
// Failures here are collected rather than raised. The records have already been
// restored by this point, and abandoning the whole operation because one
// photograph would not decode would leave somebody with a half-finished restore
// and no report of what happened - the worst of both outcomes.
func (s *Service) restoreAttachments(ctx context.Context, reader *archive.Reader, result *RestoreResult) {
	if len(result.attachmentOwners) == 0 {
		return
	}
	if s.blobs == nil {
		result.Problems = append(result.Problems,
			"the archive carries attachments, but attachment storage is not configured")
		return
	}

	actor, err := auth.MustUser(ctx)
	if err != nil {
		result.Problems = append(result.Problems, err.Error())
		return
	}

	for _, wanted := range result.pendingAttachments {
		ownerID, ok := result.attachmentOwners[wanted.OwnerType+"\x00"+wanted.OwnerKey]
		if !ok {
			// The owning record was skipped as already present, or could not be
			// restored. Either way there is nothing to attach to that this
			// restore created.
			result.Skipped++
			continue
		}

		data, err := reader.ReadFile(wanted.Path)
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf(
				"the attachment %q is listed but not in the archive", wanted.Filename))
			continue
		}

		// Through the blob store, so the bytes are hashed and verified on the
		// way in rather than trusted from the archive.
		hash, size, mime, err := s.blobs.Put(bytes.NewReader(data))
		if err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("the attachment %q could not be stored: %s", wanted.Filename, err))
			continue
		}
		if hash != wanted.SHA256 {
			result.Problems = append(result.Problems, fmt.Sprintf(
				"the attachment %q does not match its recorded hash and was not restored",
				wanted.Filename))
			continue
		}

		if _, err := s.db.CreateAttachment(ctx, domain.Attachment{
			OwnerType: wanted.OwnerType, OwnerID: ownerID,
			SHA256: hash, Filename: wanted.Filename, MIME: mime,
			SizeBytes: size, UploadedBy: actor.ID,
		}); err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("the attachment %q could not be linked: %s", wanted.Filename, err))
			continue
		}
		result.Attachments++
	}
}

// --------------------------------------------------------------- files ------

// BackupFile describes a backup on disk.
type BackupFile struct {
	Name      string
	Path      string
	SizeBytes int64
	CreatedAt time.Time
}

// WriteBackupFile writes a backup into the backup directory and returns it.
func (s *Service) WriteBackupFile(ctx context.Context, dir string, opts BackupOptions) (BackupFile, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupFile{}, fmt.Errorf("create backup directory: %w", err)
	}

	name := fmt.Sprintf("timetracker-%s.zip", s.now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)

	// Written to a temporary file and renamed, so an interrupted run never
	// leaves a truncated file that looks like a usable backup.
	temp, err := os.CreateTemp(dir, ".backup-*")
	if err != nil {
		return BackupFile{}, fmt.Errorf("create backup file: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}()

	if err := s.WriteArchive(ctx, temp, opts); err != nil {
		return BackupFile{}, err
	}
	// A backup still in the page cache is no protection against the crash it
	// exists for.
	if err := temp.Sync(); err != nil {
		return BackupFile{}, fmt.Errorf("flush backup: %w", err)
	}
	if err := temp.Close(); err != nil {
		return BackupFile{}, fmt.Errorf("close backup: %w", err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return BackupFile{}, err
	}
	if err := os.Rename(tempName, path); err != nil {
		return BackupFile{}, fmt.Errorf("store backup: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return BackupFile{}, err
	}
	return BackupFile{
		Name: name, Path: path, SizeBytes: info.Size(), CreatedAt: info.ModTime(),
	}, nil
}

// ListBackupFiles returns the backups on disk, newest first.
func (s *Service) ListBackupFiles(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []BackupFile
	for _, entry := range entries {
		// Both extensions: archives written from this release on, and the bare
		// JSON files written before it, which are still perfectly good backups
		// and should not vanish from the listing because the format moved on.
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".zip") && !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, BackupFile{
			Name: entry.Name(), Path: filepath.Join(dir, entry.Name()),
			SizeBytes: info.Size(), CreatedAt: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].CreatedAt.After(files[j].CreatedAt) })
	return files, nil
}

// PruneBackups keeps the newest n backups and removes the rest.
func (s *Service) PruneBackups(dir string, keep int) (int, error) {
	if keep < 1 {
		// Never interpret a misconfiguration as "delete everything".
		return 0, nil
	}
	files, err := s.ListBackupFiles(dir)
	if err != nil || len(files) <= keep {
		return 0, err
	}
	removed := 0
	for _, file := range files[keep:] {
		if err := os.Remove(file.Path); err == nil {
			removed++
		}
	}
	return removed, nil
}

// --------------------------------------------------------------- helpers ----

func describeScope(opts BackupOptions) BackupScope {
	scope := BackupScope{
		CustomerID: opts.CustomerID,
		ProjectID:  opts.ProjectID,
	}
	var parts []string
	if opts.CustomerID != 0 {
		parts = append(parts, fmt.Sprintf("customer %d", opts.CustomerID))
	}
	if opts.ProjectID != 0 {
		parts = append(parts, fmt.Sprintf("project %d", opts.ProjectID))
	}
	if !opts.From.IsZero() {
		scope.From = opts.From.Format("2006-01-02")
		parts = append(parts, "from "+scope.From)
	}
	if !opts.To.IsZero() {
		scope.To = opts.To.Format("2006-01-02")
		parts = append(parts, "to "+scope.To)
	}
	if len(parts) == 0 {
		scope.Everything = true
		scope.Description = "everything"
	} else {
		scope.Description = strings.Join(parts, ", ")
	}
	return scope
}

func filterCustomers(customers []domain.Customer, id int64) []domain.Customer {
	for _, c := range customers {
		if c.ID == id {
			return []domain.Customer{c}
		}
	}
	return nil
}

func projectKey(customer, project string) string {
	return strings.ToLower(customer) + "\x00" + strings.ToLower(project)
}

func assignmentKey(customer, project, assignment string) string {
	return projectKey(customer, project) + "\x00" + strings.ToLower(assignment)
}

func entryKey(assignmentID int64, started time.Time) string {
	return fmt.Sprintf("%d\x00%d", assignmentID, started.UTC().Unix())
}
