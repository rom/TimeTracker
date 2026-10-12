package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/blob"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/preview"
	"github.com/rom/timetracker/internal/store"
)

// Attachments: files and photographs on a time entry or an expense.
//
// Every path through here goes past the same authorisation check, and it is the
// check on the *owning record* rather than on the attachment: whoever may see
// the entry may see its receipt, and nobody else. The blob directory is never a
// static route, so there is no way to reach the bytes without passing this.
//
// See docs/adr/0013-attachment-storage.md.

// WithBlobs attaches a blob store to the service. Without one, attachment
// operations report that the feature is unavailable rather than failing
// obscurely.
func (s *Service) WithBlobs(store *blob.Store) *Service {
	s.blobs = store
	return s
}

// Attach stores an uploaded file against a record.
//
// The filename is used for display only; the storage path comes from the
// content hash, which is what makes traversal and case-collision problems
// structurally impossible rather than merely guarded against.
func (s *Service) Attach(ctx context.Context, ownerType string, ownerID int64, filename string, r io.Reader) (domain.Attachment, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.Attachment{}, err
	}
	if s.blobs == nil {
		return domain.Attachment{}, fmt.Errorf("%w: attachments are not configured", ErrValidation)
	}

	// Authorise against the record being attached to, before writing anything.
	if err := s.authorizeOwner(ctx, ownerType, ownerID, auth.ActionUpdate); err != nil {
		return domain.Attachment{}, err
	}

	// The bytes go to disk first and the row is written afterwards, so a crash
	// between the two leaves a harmless orphan file rather than a row pointing
	// at nothing.
	// The filename is passed so the store can check that the extension agrees
	// with the content. It never becomes part of the storage path.
	hash, size, mime, err := s.blobs.PutNamed(r, filename)
	if err != nil {
		return domain.Attachment{}, classifyBlobError(err)
	}

	attachment := domain.Attachment{
		OwnerType:  ownerType,
		OwnerID:    ownerID,
		SHA256:     hash,
		Filename:   safeDisplayName(filename),
		MIME:       mime,
		SizeBytes:  size,
		UploadedBy: actor.ID,
	}
	// The row and the audit entry commit together. If either fails, neither
	// happened and the bytes on disk are an orphan the sweep collects - which is
	// the arrangement the ordering above was chosen for.
	var created domain.Attachment
	err = s.mutate(ctx, "attachment.create", ownerType, map[string]any{
		"filename": attachment.Filename,
		"mime":     attachment.MIME,
		"bytes":    attachment.SizeBytes,
		"sha256":   attachment.SHA256,
	}, func(tx *sql.Tx) (int64, error) {
		var txErr error
		created, txErr = store.CreateAttachmentTx(ctx, tx, attachment)
		// The audit row is filed against the record the file is attached to
		// rather than against the attachment: "a receipt was added to this
		// expense" is the event somebody looks for, and the attachment's own id
		// means nothing to them.
		return ownerID, txErr
	})
	if err != nil {
		return domain.Attachment{}, err
	}
	return created, nil
}

// Attachments lists what is attached to a record.
func (s *Service) Attachments(ctx context.Context, ownerType string, ownerID int64) ([]domain.Attachment, error) {
	if err := s.authorizeOwner(ctx, ownerType, ownerID, auth.ActionView); err != nil {
		return nil, err
	}
	return s.db.ListAttachments(ctx, ownerType, ownerID)
}

// OpenAttachment authorises and opens an attachment for reading.
//
// The caller streams the result to the response. Authorisation happens here,
// against the owning record, which is the whole reason the blob directory is not
// served as static files.
func (s *Service) OpenAttachment(ctx context.Context, id int64) (domain.Attachment, io.ReadSeekCloser, error) {
	if s.blobs == nil {
		return domain.Attachment{}, nil, ErrNotFound
	}
	attachment, err := s.db.GetAttachment(ctx, id)
	if err != nil {
		return domain.Attachment{}, nil, err
	}
	if err := s.authorizeOwner(ctx, attachment.OwnerType, attachment.OwnerID, auth.ActionView); err != nil {
		return domain.Attachment{}, nil, err
	}

	reader, err := s.blobs.Get(attachment.SHA256)
	if err != nil {
		// The row exists but the file does not: a restore that missed the blobs,
		// or a sweep bug. Report it as missing to the user and loudly to the log.
		if s.log != nil {
			s.log.ErrorContext(ctx, "attachment blob is missing",
				"attachment_id", id, "sha256", attachment.SHA256)
		}
		return domain.Attachment{}, nil, ErrNotFound
	}
	return attachment, reader, nil
}

// PreviewAttachment authorises an attachment and prepares it for display.
//
// It goes through exactly the same authorisation as OpenAttachment - against
// the owning record - because a preview is a way of reading a file, and a
// second, laxer path to the bytes would make the first one pointless.
func (s *Service) PreviewAttachment(ctx context.Context, id int64) (domain.Attachment, preview.Result, error) {
	attachment, reader, err := s.OpenAttachment(ctx, id)
	if err != nil {
		return domain.Attachment{}, preview.Result{}, err
	}
	defer func() { _ = reader.Close() }()

	result, err := preview.Render(attachment.MIME, attachment.Filename, reader)
	if err != nil {
		if errors.Is(err, preview.ErrNotPreviewable) {
			// Not an error worth a page of its own: the download link is still
			// there, and the screen simply does not offer a preview.
			return attachment, preview.Result{}, ErrNotFound
		}
		return domain.Attachment{}, preview.Result{}, err
	}
	return attachment, result, nil
}

// TextPreviews extracts the text of every attachment that has any.
//
// Done in one pass here rather than one call per row from a template, because a
// template cannot report an error and a screen that silently drops a preview
// when a file has gone missing is worse than one that says so.
func (s *Service) TextPreviews(ctx context.Context, attachments []domain.Attachment) map[int64]preview.Result {
	var previews map[int64]preview.Result
	for _, attachment := range attachments {
		if Previewable(attachment) != preview.KindText {
			continue
		}
		_, result, err := s.PreviewAttachment(ctx, attachment.ID)
		if err != nil {
			// A file that has gone missing, or a document this version cannot
			// read. The row still shows its name, its size and its download
			// link; only the preview is absent.
			if s.log != nil {
				s.log.WarnContext(ctx, "an attachment could not be previewed",
					"attachment_id", attachment.ID, "error", err.Error())
			}
			continue
		}
		if previews == nil {
			previews = map[int64]preview.Result{}
		}
		previews[attachment.ID] = result
	}
	return previews
}

// TextPreview is the extracted text of one attachment, re-exported so the HTTP
// layer can name it without importing the preview package for one type.
type TextPreview = preview.Result

// Previewable reports how an attachment can be shown, without reading it.
//
// The listing calls this for every row, so it must not touch the blob store:
// deciding from the recorded type and name is what keeps a page with twenty
// receipts on it from opening twenty files.
func Previewable(attachment domain.Attachment) preview.Kind {
	return preview.Kinds(attachment.MIME, attachment.Filename)
}

// DeleteAttachment removes an attachment.
//
// The blob is removed only when nothing else references it: content addressing
// means two records can share one file, and deleting the bytes because one
// reference went would take the file away from the other.
func (s *Service) DeleteAttachment(ctx context.Context, id int64) error {
	attachment, err := s.db.GetAttachment(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authorizeOwner(ctx, attachment.OwnerType, attachment.OwnerID, auth.ActionUpdate); err != nil {
		return err
	}

	// The row goes with its audit entry, and the bytes go afterwards.
	//
	// Both halves of that matter. A failed audit write used to leave the
	// attachment deleted with nothing in the trail to say who deleted it - and
	// the bytes were already gone by then, because the removal happened before
	// the audit row was written. Removing them before the delete is final would
	// leave any other row that shares the file pointing at nothing.
	var orphaned bool
	err = s.mutate(ctx, "attachment.delete", attachment.OwnerType, map[string]any{
		"filename": attachment.Filename, "sha256": attachment.SHA256,
	}, func(tx *sql.Tx) (int64, error) {
		var txErr error
		orphaned, txErr = store.DeleteAttachmentTx(ctx, tx, id, attachment.SHA256)
		return attachment.OwnerID, txErr
	})
	if err != nil {
		return err
	}

	if orphaned && s.blobs != nil {
		if err := s.blobs.Remove(attachment.SHA256); err != nil && s.log != nil {
			// A failed blob removal wastes disk but loses no data, so it is
			// logged rather than failing the user's request. The sweep will
			// catch it.
			s.log.WarnContext(ctx, "could not remove an unreferenced blob",
				"sha256", attachment.SHA256, "error", err.Error())
		}
	}
	return nil
}

// SweepBlobs removes blobs nothing references. Run periodically.
func (s *Service) SweepBlobs(ctx context.Context) (int, error) {
	if s.blobs == nil {
		return 0, nil
	}
	referenced, err := s.db.ReferencedHashes(ctx)
	if err != nil {
		return 0, err
	}
	return s.blobs.Sweep(referenced)
}

// authorizeOwner checks the actor against the record an attachment belongs to.
func (s *Service) authorizeOwner(ctx context.Context, ownerType string, ownerID int64, action auth.Action) error {
	switch ownerType {
	case domain.AttachmentOwnerTimeEntry:
		entry, err := s.db.GetEntry(ctx, ownerID)
		if err != nil {
			return err
		}
		if err := s.authz.Can(ctx, action, auth.Resource{
			Type: "time_entry", ID: entry.ID, OwnerID: entry.UserID,
			ProjectID: entry.ProjectID, CustomerID: entry.CustomerID,
		}); err != nil {
			return notFoundFor(err)
		}
		return nil

	case domain.AttachmentOwnerExpense:
		expense, err := s.db.GetExpense(ctx, ownerID)
		if err != nil {
			return err
		}
		if err := s.authz.Can(ctx, action, auth.Resource{
			Type: "expense", ID: expense.ID, OwnerID: expense.UserID,
			ProjectID: expense.ProjectID, CustomerID: expense.CustomerID,
		}); err != nil {
			return notFoundFor(err)
		}
		return nil

	default:
		return fmt.Errorf("%w: unknown attachment owner %q", ErrValidation, ownerType)
	}
}

// safeDisplayName reduces a client-supplied filename to something safe to show.
//
// It is never a path component - the storage path is the content hash - so this
// is about display rather than traversal. Directory parts are stripped anyway,
// because a name containing a path reads as a mistake and looks alarming.
func safeDisplayName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(name)

	// Control characters in a filename are either a mistake or an attempt to
	// spoof an extension by hiding the real one.
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)

	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

// classifyBlobError turns a store error into one the web layer maps correctly.
func classifyBlobError(err error) error {
	switch {
	case strings.Contains(err.Error(), blob.ErrTooLarge.Error()):
		return fmt.Errorf("%w: %s", ErrValidation, err)
	case strings.Contains(err.Error(), blob.ErrUnacceptableType.Error()),
		strings.Contains(err.Error(), blob.ErrTypeMismatch.Error()):
		return fmt.Errorf("%w: %s", ErrValidation, err)
	default:
		return err
	}
}
