package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Undoing a deletion.
//
// The catalogue has never needed this: a customer, project or assignment is
// archived and the admin screen has always offered Restore beside Archive,
// because an invoice issued last year must still be able to say who was billed.
// Everything else was destroyed outright, and the audit log recorded that it
// had happened along with a partial description - enough to answer "who deleted
// my Tuesday", not enough to put Tuesday back.
//
// What makes this safe rather than a second source of truth: the capture and
// the delete are one transaction, so a capture that failed cannot leave a
// record destroyed and unrecoverable, and a delete that failed cannot leave a
// ghost in the bin. The audit row is written in the same transaction as before,
// unchanged; the bin is not a replacement for the trail, and the trail is still
// what says who did it.

// binOrder lists the tables of each kind in the order they must be written
// back: a parent before the rows that reference it.
//
// This is also the allow-list. A payload naming a table that is not here is
// refused rather than half-restored, which is what happens when a record
// deleted by a newer version of the application is restored by an older one.
var binOrder = map[string][]string{
	"time_entry":    {"time_entries", "entry_tags"},
	"expense":       {"expenses"},
	"tag":           {"tags", "entry_tags"},
	"routine":       {"routines"},
	"contract_term": {"contract_terms"},
	"absence":       {"absences"},
}

// DeletedRecord is a thing in the bin, re-exported for the HTTP layer.
type DeletedRecord = store.DeletedRecord

// binDeletion captures a record and its children, then runs the caller's
// deletion, in one transaction with the audit row.
//
// Every destructive path goes through here. That is the whole mechanism: a new
// way to delete something either calls this or its records are gone for good,
// which is exactly the property checkPeriodOpen has for the week lock.
func (s *Service) binDeletion(
	ctx context.Context,
	kind string, recordID, ownerID int64, summary string,
	captures []store.Capture,
	auditAction, auditType string, detail map[string]any,
	remove func(ctx context.Context, tx *sql.Tx) error,
) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	if _, known := binOrder[kind]; !known {
		return fmt.Errorf("%w: nothing knows how to restore a %s", ErrValidation, kind)
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		// Captured before the delete, in the same transaction, so the two
		// cannot disagree.
		if _, txErr := store.CaptureTx(ctx, tx, store.DeletedRecord{
			Kind: kind, RecordID: recordID, UserID: ownerID, Summary: summary,
			DeletedBy: actor.ID, DeletedAt: s.now(),
		}, captures); txErr != nil {
			return txErr
		}
		if txErr := remove(ctx, tx); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, auditAction, auditType, recordID, 0, detail)
	})
	if err != nil {
		return err
	}

	s.auditLog(ctx, auditAction, auditType, recordID)
	return nil
}

// RecentlyDeleted lists what the acting user may put back.
//
// Scoped like everything else. A person sees what was theirs; records that
// belong to the instance rather than to a person - a tag is shared by every
// project - are shown only to somebody who may administer the instance, which
// is the same bound that decides who may delete one.
func (s *Service) RecentlyDeleted(ctx context.Context) ([]DeletedRecord, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	// A client has no deletions of their own and no business reading anybody
	// else's: the role is read-only by definition (ADR-0008).
	if actor.Role == domain.RoleClient {
		return nil, nil
	}
	return s.db.ListDeleted(ctx, actor.ID, s.MayAdministerInstance(ctx), 100)
}

// RestoreDeleted puts a deleted record back.
func (s *Service) RestoreDeleted(ctx context.Context, id int64) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	record, err := s.db.GetDeleted(ctx, id)
	if err != nil {
		return err
	}

	// Who may put it back is who may have deleted it. A record with an owner
	// belongs to that person; one without - a tag - belongs to the instance.
	if record.UserID == 0 {
		if !s.MayAdministerInstance(ctx) {
			return notFoundFor(fmt.Errorf("%w: the instance is the administrator's "+
				"to administer", ErrForbidden))
		}
	} else if record.UserID != actor.ID {
		if err := s.authz.Can(ctx, auth.ActionDelete, auth.Resource{
			Type: "time_entry", OwnerID: record.UserID,
		}); err != nil {
			return notFoundFor(err)
		}
	}

	order, known := binOrder[record.Kind]
	if !known {
		return fmt.Errorf("%w: nothing knows how to restore a %s",
			ErrValidation, record.Kind)
	}

	// A week that has since been submitted or approved is not a week to put
	// hours back into without somebody deciding to. The lock is checked on the
	// restored entry's own date, exactly as it is on a new one.
	if record.Kind == "time_entry" {
		when, found := restoredStart(record)
		if found {
			if err := s.checkPeriodOpen(ctx, record.UserID, when); err != nil {
				return err
			}
		}
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if txErr := store.RestoreTx(ctx, tx, record, order); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, record.Kind+".restore", record.Kind, record.RecordID, 0,
			map[string]any{"summary": record.Summary, "deleted_at": record.DeletedAt})
	})
	if err != nil {
		return err
	}

	s.auditLog(ctx, record.Kind+".restore", record.Kind, record.RecordID)
	return nil
}

// restoredStart reads the start time out of a captured time entry, so the week
// lock can be checked before putting it back.
func restoredStart(record DeletedRecord) (time.Time, bool) {
	rows, err := store.PayloadRows(record.Payload, "time_entries")
	if err != nil || len(rows) == 0 {
		return time.Time{}, false
	}
	value, ok := rows[0]["started_at"]
	if !ok || value == nil {
		return time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return time.Time{}, false
	}
	return when, true
}
