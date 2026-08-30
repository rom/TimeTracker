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

// StartTimer begins a new running entry on an assignment.
//
// It deliberately does not stop anything else. Several timers may run at once,
// because work genuinely happens in parallel - a build running for one client
// while a meeting for another is under way. See docs/adr/0004-concurrent-timers.md.
func (s *Service) StartTimer(ctx context.Context, assignmentID int64, note string) (domain.TimeEntry, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.TimeEntry{}, err
	}

	assignment, err := s.db.GetAssignment(ctx, assignmentID)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	if assignment.Archived() {
		return domain.TimeEntry{}, fmt.Errorf("%w: %q has been archived", ErrValidation, assignment.Name)
	}

	if err := s.authz.Can(ctx, auth.ActionCreate, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
		ProjectID: assignment.ProjectID, CustomerID: assignment.CustomerID,
	}); err != nil {
		return domain.TimeEntry{}, err
	}
	// A timer started now lands in the current week, so a locked week refuses it
	// like any other new time. Catching it here rather than at Stop means the
	// person is told before they work rather than after.
	if err := s.checkPeriodOpen(ctx, actor.ID, s.now()); err != nil {
		return domain.TimeEntry{}, err
	}

	entry := domain.TimeEntry{
		UserID:       actor.ID,
		EnteredBy:    actor.ID,
		AssignmentID: assignmentID,
		StartedAt:    s.now(),
		Note:         note,
		Billable:     assignment.BillableDefault,
		Status:       domain.StatusConfirmed,
		TimeZone:     userZone(actor),
	}
	if err := entry.Validate(); err != nil {
		return domain.TimeEntry{}, err
	}

	var created domain.TimeEntry
	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		if created, txErr = createEntryTx(ctx, tx, entry); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, "time_entry.start", "time_entry", created.ID, 0, map[string]any{
			"assignment": assignment.Label(),
			"started_at": created.StartedAt,
		})
	})
	if err != nil {
		return domain.TimeEntry{}, err
	}

	s.auditLog(ctx, "time_entry.start", "time_entry", created.ID)
	return s.db.GetEntry(ctx, created.ID)
}

// StopTimer stops a running entry and computes its duration.
//
// Stopping is idempotent: a double-click, or two browser tabs racing, results in
// one stop and one duration. The conditional update in the store is what makes
// that true; here we simply report the already-stopped entry rather than treating
// it as an error the user has to understand.
func (s *Service) StopTimer(ctx context.Context, entryID int64) (domain.TimeEntry, error) {
	entry, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	if err := s.canModify(ctx, entry); err != nil {
		return domain.TimeEntry{}, notFoundFor(err)
	}
	if !entry.Running() {
		return entry, nil
	}
	// A timer started before the week was submitted would otherwise add its
	// hours to a locked week when it stopped.
	if err := s.checkPeriodOpen(ctx, entry.UserID, entry.StartedAt); err != nil {
		return domain.TimeEntry{}, err
	}

	endedAt := s.now()
	seconds := domain.SecondsBetween(entry.StartedAt, endedAt)
	if seconds < 0 {
		// The clock moved backwards, or the entry was edited to start in the
		// future. Recording a negative duration would poison every total, so
		// clamp and flag it for a human instead.
		seconds = 0
	}

	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return domain.TimeEntry{}, err
	}

	// Resolve the rate and rounding rule now, while stopping, and store the
	// result on the entry. Doing it here rather than at report time is what
	// makes an invoiced figure stable against a later rate change.
	billed := entry
	billed.DurationSeconds = seconds
	if err := s.applyBillingTo(ctx, &billed); err != nil {
		return domain.TimeEntry{}, err
	}
	snapshot := store.BillingSnapshot{
		RoundingRule:    billed.RoundingRuleApplied,
		BillableSeconds: billed.BillableSeconds,
		RateMinor:       billed.RateMinor,
		AmountMinor:     billed.AmountMinor,
		Currency:        billed.Currency,
	}
	// A timer left running overnight is the dominant failure mode of any tracker.
	// Rather than silently billing it, mark it for review; flagged entries are
	// excluded from totals until a human resolves them.
	flagged := settings.MaxTimerSeconds > 0 && seconds > settings.MaxTimerSeconds

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		stopped, txErr := store.StopEntryTx(ctx, tx, entryID, endedAt, seconds, snapshot)
		if txErr != nil {
			return txErr
		}
		if !stopped {
			// Another request stopped it between our read and our write. That is
			// a success, not a conflict - but we must not write an audit row for
			// a change we did not make.
			return nil
		}
		if flagged {
			if _, txErr = tx.ExecContext(ctx,
				`UPDATE time_entries SET flagged = 1 WHERE id = ?`, entryID); txErr != nil {
				return txErr
			}
		}
		return s.audit(ctx, tx, "time_entry.stop", "time_entry", entryID, 0, map[string]any{
			"duration_seconds": seconds,
			"billable_seconds": snapshot.BillableSeconds,
			"amount_minor":     snapshot.AmountMinor,
			"currency":         snapshot.Currency,
			"flagged":          flagged,
		})
	})
	if err != nil {
		return domain.TimeEntry{}, err
	}

	s.auditLog(ctx, "time_entry.stop", "time_entry", entryID)
	return s.db.GetEntry(ctx, entryID)
}

// StopAllTimers stops every running timer for the acting user, for the
// end-of-day case where several were left going.
func (s *Service) StopAllTimers(ctx context.Context) (int, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return 0, err
	}
	running, err := s.db.ListRunningEntries(ctx, actor.ID)
	if err != nil {
		return 0, err
	}
	stopped := 0
	for _, entry := range running {
		if _, err := s.StopTimer(ctx, entry.ID); err != nil {
			return stopped, err
		}
		stopped++
	}
	return stopped, nil
}

// EntryInput is the data a user supplies when creating or editing an entry by
// hand, as opposed to running a timer.
type EntryInput struct {
	AssignmentID int64
	StartedAt    time.Time
	// DurationSeconds is used when EndedAt is not given, which is the common case
	// for manual entry ("2h on the migration").
	DurationSeconds int64
	EndedAt         *time.Time
	Note            string
	Billable        bool
	// Kind is work, overtime or travel. Empty means work, so a caller that does
	// not know about kinds records ordinary work rather than nothing.
	Kind domain.EntryKind
	// Tags are the complete set for the entry: what is saved replaces what was
	// there, because the form shows every tag and a removed one has to go.
	Tags []string
	// OnBehalfOf, when set to another user, makes this a proxy proposal that
	// requires that user's confirmation before it counts.
	OnBehalfOf int64
}

// CreateEntry records time that has already happened.
func (s *Service) CreateEntry(ctx context.Context, in EntryInput) (domain.TimeEntry, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.TimeEntry{}, err
	}

	assignment, err := s.db.GetAssignment(ctx, in.AssignmentID)
	if err != nil {
		return domain.TimeEntry{}, err
	}

	// Whose time is this? Proxy entries name a different subject, and are subject
	// to a different permission and a different starting status.
	subjectID := actor.ID
	status := domain.StatusConfirmed
	action := auth.ActionCreate
	if in.OnBehalfOf != 0 && in.OnBehalfOf != actor.ID {
		subjectID = in.OnBehalfOf
		// Time recorded in someone else's name does not count until they accept
		// it. See docs/adr/0005-proxy-time-entry.md.
		status = domain.StatusPending
		action = auth.ActionProxy
	}

	if err := s.authz.Can(ctx, action, auth.Resource{
		Type: "time_entry", OwnerID: subjectID,
		ProjectID: assignment.ProjectID, CustomerID: assignment.CustomerID,
	}); err != nil {
		return domain.TimeEntry{}, err
	}

	subject := actor
	if subjectID != actor.ID {
		if subject, err = s.db.GetUser(ctx, subjectID); err != nil {
			return domain.TimeEntry{}, err
		}
	}

	entry := domain.TimeEntry{
		UserID:       subjectID,
		EnteredBy:    actor.ID,
		AssignmentID: in.AssignmentID,
		StartedAt:    in.StartedAt,
		Note:         in.Note,
		Billable:     in.Billable,
		Kind:         in.Kind,
		Tags:         in.Tags,
		Status:       status,
		// The entry's own zone decides which day it belongs to, so it is the
		// subject's zone rather than the reader's.
		TimeZone: userZone(subject),
	}
	applyEnd(&entry, in)

	if err := entry.Validate(); err != nil {
		return domain.TimeEntry{}, err
	}
	// Adding work to a week somebody has already declared finished would change
	// a figure that has been submitted or approved.
	if err := s.checkPeriodOpen(ctx, entry.UserID, entry.StartedAt); err != nil {
		return domain.TimeEntry{}, err
	}
	if err := s.applyBillingTo(ctx, &entry); err != nil {
		return domain.TimeEntry{}, err
	}

	var created domain.TimeEntry
	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		if created, txErr = createEntryTx(ctx, tx, entry); txErr != nil {
			return txErr
		}
		onBehalfOf := int64(0)
		if entry.IsProxy() {
			onBehalfOf = entry.UserID
		}
		return s.audit(ctx, tx, "time_entry.create", "time_entry", created.ID, onBehalfOf, map[string]any{
			"assignment":       assignment.Label(),
			"duration_seconds": created.DurationSeconds,
			"status":           string(created.Status),
		})
	})
	if err != nil {
		return domain.TimeEntry{}, err
	}

	s.auditLog(ctx, "time_entry.create", "time_entry", created.ID)
	return s.db.GetEntry(ctx, created.ID)
}

// UpdateEntry saves an edit to an existing entry.
func (s *Service) UpdateEntry(ctx context.Context, entryID int64, in EntryInput) (domain.TimeEntry, error) {
	existing, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	if err := s.canModify(ctx, existing); err != nil {
		return domain.TimeEntry{}, notFoundFor(err)
	}
	// Both weeks are checked, because an edit can move an entry between them:
	// taking an hour out of an approved week is as much a change to it as
	// editing one inside it.
	if err := s.checkPeriodOpen(ctx, existing.UserID, existing.StartedAt); err != nil {
		return domain.TimeEntry{}, err
	}
	if !in.StartedAt.IsZero() && !sameWeek(existing.StartedAt, in.StartedAt) {
		if err := s.checkPeriodOpen(ctx, existing.UserID, in.StartedAt); err != nil {
			return domain.TimeEntry{}, err
		}
	}

	assignment, err := s.db.GetAssignment(ctx, in.AssignmentID)
	if err != nil {
		return domain.TimeEntry{}, err
	}

	updated := existing
	updated.AssignmentID = in.AssignmentID
	updated.StartedAt = in.StartedAt
	updated.Note = in.Note
	updated.Billable = in.Billable
	updated.Kind = in.Kind
	updated.Tags = in.Tags
	// Editing an entry clears a review flag: a human has now looked at it, which
	// is exactly what the flag was asking for.
	updated.Flagged = false
	applyEnd(&updated, in)

	if err := updated.Validate(); err != nil {
		return domain.TimeEntry{}, err
	}
	// An edited duration or a changed assignment changes what the entry is
	// worth, so the snapshot is recomputed rather than left stale.
	if err := s.applyBillingTo(ctx, &updated); err != nil {
		return domain.TimeEntry{}, err
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if txErr := updateEntryTx(ctx, tx, updated); txErr != nil {
			return txErr
		}
		// The audit detail records what actually changed rather than the whole
		// row, so the trail stays readable when someone is looking for the edit
		// that altered an invoiced figure.
		return s.audit(ctx, tx, "time_entry.update", "time_entry", entryID, 0,
			entryDiff(existing, updated, assignment.Label()))
	})
	if err != nil {
		return domain.TimeEntry{}, err
	}

	s.auditLog(ctx, "time_entry.update", "time_entry", entryID)
	return s.db.GetEntry(ctx, entryID)
}

// DeleteEntry removes an entry. The prior state is recorded in the audit trail,
// so a deletion is recoverable as information even though the row is gone.
func (s *Service) DeleteEntry(ctx context.Context, entryID int64) error {
	existing, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return err
	}
	if err := s.canDelete(ctx, existing); err != nil {
		return notFoundFor(err)
	}
	if err := s.checkPeriodOpen(ctx, existing.UserID, existing.StartedAt); err != nil {
		return err
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM time_entries WHERE id = ?`, entryID); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, "time_entry.delete", "time_entry", entryID, 0, map[string]any{
			"assignment":       existing.AssignmentName,
			"started_at":       existing.StartedAt,
			"duration_seconds": existing.DurationSeconds,
			"note":             existing.Note,
		})
	})
	if err != nil {
		return err
	}

	s.auditLog(ctx, "time_entry.delete", "time_entry", entryID)
	return nil
}

// Entry loads one entry, authorised.
func (s *Service) Entry(ctx context.Context, entryID int64) (domain.TimeEntry, error) {
	entry, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, entryResource(entry)); err != nil {
		return domain.TimeEntry{}, notFoundFor(err)
	}
	return s.narrowEntry(ctx, entry), nil
}

// RunningTimers returns every timer currently running for the acting user. The
// header renders this on every page, so losing track of a timer is hard.
func (s *Service) RunningTimers(ctx context.Context) ([]domain.TimeEntry, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	// clientResource rather than a bare owner, because a client's permission is
	// their customer and not the ownership of a record. Asked the ownership
	// question, the authoriser correctly refuses - and this is called by the
	// page shell, so every screen returned 403 to a client before it rendered
	// anything. A read-only portal that cannot open a page is not a portal.
	if err := s.authz.Can(ctx, auth.ActionView, clientResource(ctx, "time_entry")); err != nil {
		return nil, err
	}
	if actingAsClient(ctx) {
		// A client runs no timers: they may not start one. The header has
		// nothing to show them, and asking the database is a query with a known
		// answer on every page render.
		return nil, nil
	}
	entries, err := s.db.ListRunningEntries(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	// A client runs no timers, so this is empty for them. Narrowed regardless:
	// every path that can return an entry applies the projection, so the
	// guarantee holds by construction rather than by which filter happens to be
	// in the query today.
	return s.narrowEntries(ctx, entries), nil
}

// ------------------------------------------------------------------ helpers --

// canModify checks whether the actor may change an entry.
func (s *Service) canModify(ctx context.Context, entry domain.TimeEntry) error {
	return s.authz.Can(ctx, auth.ActionUpdate, entryResource(entry))
}

// canDelete checks whether the actor may remove an entry.
func (s *Service) canDelete(ctx context.Context, entry domain.TimeEntry) error {
	return s.authz.Can(ctx, auth.ActionDelete, entryResource(entry))
}

// entryResource describes an entry for an authorisation decision. OwnerID is the
// subject of the entry, not whoever typed it in, which is what stops a proxy
// author from retaining editing rights over someone else's confirmed time.
func entryResource(e domain.TimeEntry) auth.Resource {
	return auth.Resource{Type: "time_entry", ID: e.ID, OwnerID: e.UserID}
}

// applyEnd fills in the end time and duration from whichever the caller supplied.
func applyEnd(entry *domain.TimeEntry, in EntryInput) {
	switch {
	case in.EndedAt != nil:
		end := *in.EndedAt
		entry.EndedAt = &end
		entry.DurationSeconds = domain.SecondsBetween(entry.StartedAt, end)
	case in.DurationSeconds > 0:
		end := entry.StartedAt.Add(time.Duration(in.DurationSeconds) * time.Second)
		entry.EndedAt = &end
		entry.DurationSeconds = in.DurationSeconds
	default:
		// Neither given: the entry is left running.
		entry.EndedAt = nil
		entry.DurationSeconds = 0
	}
}

// sameWeek reports whether two instants fall in the same ISO week.
//
// Used to avoid a second period lookup when an edit does not move the entry
// between weeks, which is the overwhelmingly common case.
func sameWeek(a, b time.Time) bool {
	yearA, weekA := a.ISOWeek()
	yearB, weekB := b.ISOWeek()
	return yearA == yearB && weekA == weekB
}

// userZone returns a user's IANA zone, defaulting to UTC. An empty zone would
// otherwise silently attribute entries to the wrong calendar day.
func userZone(u domain.User) string {
	if u.TimeZone == "" {
		return "UTC"
	}
	return u.TimeZone
}

// entryDiff builds the compact description of an edit for the audit trail,
// listing only the fields that actually changed.
func entryDiff(before, after domain.TimeEntry, assignmentLabel string) map[string]any {
	diff := map[string]any{}
	if before.AssignmentID != after.AssignmentID {
		diff["assignment"] = map[string]any{"from": before.AssignmentName, "to": assignmentLabel}
	}
	if !before.StartedAt.Equal(after.StartedAt) {
		diff["started_at"] = map[string]any{"from": before.StartedAt, "to": after.StartedAt}
	}
	if before.DurationSeconds != after.DurationSeconds {
		diff["duration_seconds"] = map[string]any{"from": before.DurationSeconds, "to": after.DurationSeconds}
	}
	if before.Note != after.Note {
		diff["note"] = map[string]any{"from": before.Note, "to": after.Note}
	}
	if before.Billable != after.Billable {
		diff["billable"] = map[string]any{"from": before.Billable, "to": after.Billable}
	}
	if before.Flagged != after.Flagged {
		diff["flagged"] = map[string]any{"from": before.Flagged, "to": after.Flagged}
	}
	return diff
}

// createEntryTx and updateEntryTx insert and update inside the caller's
// transaction, so a change and its audit row commit together.
//
// They delegate to the store rather than carrying their own SQL. They used to
// carry it, and the two copies drifted: a column added to the store's insert
// was silently missing from this one, so a field the application believed it was
// storing was not stored at all. Column knowledge belongs in exactly one
// package, and this is how the service reaches it without keeping a second copy.
func createEntryTx(ctx context.Context, tx *sql.Tx, e domain.TimeEntry) (domain.TimeEntry, error) {
	return store.CreateEntryWithTagsTx(ctx, tx, e)
}

func updateEntryTx(ctx context.Context, tx *sql.Tx, e domain.TimeEntry) error {
	return store.UpdateEntryWithTagsTx(ctx, tx, e)
}
