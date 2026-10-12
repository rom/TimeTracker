package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Attachment metadata. The bytes live on disk in the blob store; this table
// records what they are and what they belong to.
// See docs/adr/0013-attachment-storage.md.

// CreateAttachment records an uploaded file.
func (db *DB) CreateAttachment(ctx context.Context, a domain.Attachment) (domain.Attachment, error) {
	return CreateAttachmentTx(ctx, db.write, a)
}

// CreateAttachmentTx records an uploaded file on the caller's executor, so the
// row and its audit entry can commit together.
//
// The bytes are not part of this. They are written to the blob store first and
// content-addressed, so a transaction that rolls back leaves a file nothing
// references - which the orphan sweep collects, and which is harmless in the
// meantime. The reverse order would leave a row pointing at bytes that are not
// there, which is not harmless at all.
func CreateAttachmentTx(ctx context.Context, db Execer, a domain.Attachment) (domain.Attachment, error) {
	now := time.Now()
	res, err := db.ExecContext(ctx, `
		INSERT INTO attachments (owner_type, owner_id, sha256, filename, mime,
		                         size_bytes, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.OwnerType, a.OwnerID, a.SHA256, a.Filename, a.MIME, a.SizeBytes,
		a.UploadedBy, formatTime(now))
	if err != nil {
		return domain.Attachment{}, fmt.Errorf("create attachment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Attachment{}, err
	}
	a.ID = id
	a.CreatedAt = now
	return a, nil
}

// GetAttachment loads one attachment's metadata.
func (db *DB) GetAttachment(ctx context.Context, id int64) (domain.Attachment, error) {
	var a domain.Attachment
	var createdAt string
	err := db.read.QueryRowContext(ctx, `
		SELECT id, owner_type, owner_id, sha256, filename, mime, size_bytes,
		       uploaded_by, created_at
		FROM attachments WHERE id = ?`, id).
		Scan(&a.ID, &a.OwnerType, &a.OwnerID, &a.SHA256, &a.Filename, &a.MIME,
			&a.SizeBytes, &a.UploadedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attachment{}, ErrNotFound
	}
	if err != nil {
		return domain.Attachment{}, err
	}
	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Attachment{}, err
	}
	return a, nil
}

// ListAttachments returns everything attached to one record.
func (db *DB) ListAttachments(ctx context.Context, ownerType string, ownerID int64) ([]domain.Attachment, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT id, owner_type, owner_id, sha256, filename, mime, size_bytes,
		       uploaded_by, created_at
		FROM attachments WHERE owner_type = ? AND owner_id = ?
		ORDER BY created_at, id`, ownerType, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attachments []domain.Attachment
	for rows.Next() {
		var a domain.Attachment
		var createdAt string
		if err := rows.Scan(&a.ID, &a.OwnerType, &a.OwnerID, &a.SHA256, &a.Filename,
			&a.MIME, &a.SizeBytes, &a.UploadedBy, &createdAt); err != nil {
			return nil, err
		}
		var perr error
		if a.CreatedAt, perr = parseTime(createdAt); perr != nil {
			return nil, perr
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

// DeleteAttachment removes one reference and reports whether the underlying blob
// is now unreferenced.
//
// The caller sweeps the blob only when this says so: content addressing means
// several records can share one file, and deleting the bytes because one
// reference went would take the file away from the others.
func (db *DB) DeleteAttachment(ctx context.Context, id int64) (hash string, orphaned bool, err error) {
	attachment, err := db.GetAttachment(ctx, id)
	if err != nil {
		return "", false, err
	}

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		orphaned, err = DeleteAttachmentTx(ctx, tx, id, attachment.SHA256)
		return err
	})
	return attachment.SHA256, orphaned, err
}

// DeleteAttachmentTx removes one reference on the caller's transaction and
// reports whether the blob is now unreferenced.
//
// The hash is passed in rather than read here, because the caller has already
// loaded the attachment to authorise against its owning record - and a read
// inside the transaction would be a second lookup of a row this is about to
// delete.
//
// Whether the blob is orphaned is decided inside the transaction and acted on
// outside it, after the commit. Removing the bytes first would leave the
// remaining row pointing at nothing if the transaction then rolled back.
func DeleteAttachmentTx(ctx context.Context, tx *sql.Tx, id int64, hash string) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE id = ?`, id); err != nil {
		return false, err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM attachments WHERE sha256 = ?`, hash).Scan(&remaining); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

// ReferencedHashes returns every blob hash still in use, for the orphan sweep.
func (db *DB) ReferencedHashes(ctx context.Context) (map[string]bool, error) {
	rows, err := db.read.QueryContext(ctx, `SELECT DISTINCT sha256 FROM attachments`)
	if err != nil {
		return nil, fmt.Errorf("list referenced blobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hashes := map[string]bool{}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		hashes[hash] = true
	}
	return hashes, rows.Err()
}
