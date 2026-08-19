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

// The proxy confirmation workflow.
//
// A colleague can record time in your name - useful when you worked together
// and one of you tracks time properly. It does not become your time until you
// say so: until then the entry counts for nothing, in any total, report or
// export. See docs/adr/0005-proxy-time-entry.md and ASR-008.
//
// The rules enforced here:
//
//   - only the subject decides. Not the author, not an administrator - somebody
//     else accepting on your behalf would defeat the entire point.
//   - a rejection is recorded, with its reason, rather than deleted. The record
//     of what was claimed has to survive the disagreement.
//   - a decision is final. Re-proposing is a new entry, so the trail shows two
//     proposals rather than one that changed its mind.

// Inbox is what is waiting for a user's decision.
type Inbox struct {
	// Entries are proposals of time in this user's name.
	Entries []domain.TimeEntry
	// Expenses are the same for costs.
	Expenses []domain.Expense
	// Overlapping marks entry ids that overlap another pending proposal, which
	// usually means two people proposed the same work.
	Overlapping map[int64]bool
}

// Count returns how many items need a decision, for the navigation badge.
func (i Inbox) Count() int { return len(i.Entries) + len(i.Expenses) }

// PendingCount is Count without building the inbox.
//
// The badge appears on every screen, so it was fetching every pending entry
// with its assignment, project, customer, both users and an attachment count -
// to render a number. Against a hundred thousand entries that measured 100 ms
// per page (ASR-012, docs/adr/0032-measured-before-tuned.md).
func (s *Service) PendingCount(ctx context.Context) (int, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return 0, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return 0, err
	}
	return s.db.CountPending(ctx, actor.ID, scope)
}

// Inbox returns everything awaiting the acting user's decision.
func (s *Service) Inbox(ctx context.Context) (Inbox, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return Inbox{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return Inbox{}, err
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return Inbox{}, err
	}

	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		UserID:   actor.ID,
		Statuses: []domain.EntryStatus{domain.StatusPending},
		Scope:    scope,
	})
	if err != nil {
		return Inbox{}, err
	}
	expenses, err := s.db.ListExpenses(ctx, store.ExpenseFilter{
		UserID:   actor.ID,
		Statuses: []domain.EntryStatus{domain.StatusPending},
		Scope:    scope,
	})
	if err != nil {
		return Inbox{}, err
	}

	return Inbox{
		Entries:     entries,
		Expenses:    expenses,
		Overlapping: findOverlappingProposals(entries),
	}, nil
}

// findOverlappingProposals marks proposals that cover the same time.
//
// Two people proposing for the same period is the common way this goes wrong -
// a manager and a colleague both being helpful - and accepting both would double
// the hours. Flagging is enough; deciding which is right is the subject's job.
func findOverlappingProposals(entries []domain.TimeEntry) map[int64]bool {
	overlapping := map[int64]bool{}
	for i, a := range entries {
		if a.EndedAt == nil {
			continue
		}
		intervalA := domain.Interval{Start: a.StartedAt.Unix(), End: a.EndedAt.Unix()}
		for _, b := range entries[i+1:] {
			if b.EndedAt == nil {
				continue
			}
			intervalB := domain.Interval{Start: b.StartedAt.Unix(), End: b.EndedAt.Unix()}
			if intervalA.Overlaps(intervalB) {
				overlapping[a.ID] = true
				overlapping[b.ID] = true
			}
		}
	}
	return overlapping
}

// AcceptEntry confirms a proposal, making it count.
func (s *Service) AcceptEntry(ctx context.Context, entryID int64) (domain.TimeEntry, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	entry, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	if err := s.canDecide(ctx, actor, entry); err != nil {
		return domain.TimeEntry{}, err
	}

	// Recompute the billing snapshot at acceptance rather than trusting what the
	// proposer's context produced: the rate that applies is the subject's on
	// their project, not the author's.
	accepted := entry
	accepted.Status = domain.StatusConfirmed
	accepted.DecidedBy = actor.ID
	accepted.DecidedAt = s.now()
	if err := s.applyBillingTo(ctx, &accepted); err != nil {
		return domain.TimeEntry{}, err
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `
			UPDATE time_entries SET status = ?, decided_by = ?, decided_at = ?,
			       rounding_rule_applied = ?, billable_seconds = ?, rate_minor = ?,
			       amount_minor = ?, currency = ?, updated_at = ?
			WHERE id = ? AND status = ?`,
			string(domain.StatusConfirmed), actor.ID, accepted.DecidedAt.UTC().Format(time.RFC3339),
			accepted.RoundingRuleApplied, accepted.BillableSeconds, accepted.RateMinor,
			accepted.AmountMinor, accepted.Currency, s.now().UTC().Format(time.RFC3339),
			entryID, string(domain.StatusPending)); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, "time_entry.accept", "time_entry", entryID, entry.EnteredBy,
			map[string]any{
				"proposed_by":      entry.EnteredByName,
				"duration_seconds": entry.DurationSeconds,
			})
	})
	if err != nil {
		return domain.TimeEntry{}, err
	}
	s.auditLog(ctx, "time_entry.accept", "time_entry", entryID)
	return s.db.GetEntry(ctx, entryID)
}

// RejectEntry declines a proposal.
//
// The entry is kept, not deleted: the record of what was claimed has to survive
// the disagreement, and the author needs to see that it was declined rather than
// silently vanishing.
func (s *Service) RejectEntry(ctx context.Context, entryID int64, reason string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	entry, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return err
	}
	if err := s.canDecide(ctx, actor, entry); err != nil {
		return err
	}
	if len(reason) > 1000 {
		return fmt.Errorf("%w: the reason is too long", ErrValidation)
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `
			UPDATE time_entries SET status = ?, decided_by = ?, decided_at = ?,
			       decision_note = ?, updated_at = ?
			WHERE id = ? AND status = ?`,
			string(domain.StatusRejected), actor.ID, s.now().UTC().Format(time.RFC3339),
			reason, s.now().UTC().Format(time.RFC3339),
			entryID, string(domain.StatusPending)); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, "time_entry.reject", "time_entry", entryID, entry.EnteredBy,
			map[string]any{"proposed_by": entry.EnteredByName, "reason": reason})
	})
	if err != nil {
		return err
	}
	s.auditLog(ctx, "time_entry.reject", "time_entry", entryID)
	return nil
}

// AcceptExpense confirms a proposed cost.
func (s *Service) AcceptExpense(ctx context.Context, expenseID int64) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	expense, err := s.db.GetExpense(ctx, expenseID)
	if err != nil {
		return err
	}
	if expense.UserID != actor.ID {
		// Only the subject decides. Reported as not-found rather than forbidden,
		// consistent with everything else the actor has no business seeing.
		return ErrNotFound
	}
	if expense.Status != domain.StatusPending {
		return fmt.Errorf("%w: this expense has already been decided", ErrConflict)
	}

	expense.Status = domain.StatusConfirmed
	if err := s.db.UpdateExpense(ctx, expense); err != nil {
		return err
	}
	return s.recordAudit(ctx, "expense.accept", "expense", expenseID, map[string]any{
		"proposed_by": expense.EnteredByName,
	})
}

// RejectExpense declines a proposed cost, keeping the record.
func (s *Service) RejectExpense(ctx context.Context, expenseID int64, reason string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	expense, err := s.db.GetExpense(ctx, expenseID)
	if err != nil {
		return err
	}
	if expense.UserID != actor.ID {
		return ErrNotFound
	}
	if expense.Status != domain.StatusPending {
		return fmt.Errorf("%w: this expense has already been decided", ErrConflict)
	}

	expense.Status = domain.StatusRejected
	if err := s.db.UpdateExpense(ctx, expense); err != nil {
		return err
	}
	return s.recordAudit(ctx, "expense.reject", "expense", expenseID, map[string]any{
		"proposed_by": expense.EnteredByName,
		"reason":      reason,
	})
}

// canDecide checks that this actor may decide on this proposal.
//
// The rule is narrow on purpose: only the subject, and only while it is pending.
// An administrator cannot accept on someone's behalf, because a consent that
// someone else can give is not consent.
func (s *Service) canDecide(_ context.Context, actor domain.User, entry domain.TimeEntry) error {
	if entry.UserID != actor.ID {
		// Not "forbidden": an entry the actor has no business seeing should look
		// the same as one that does not exist.
		return ErrNotFound
	}
	if entry.Status != domain.StatusPending {
		return fmt.Errorf("%w: this entry has already been decided", ErrConflict)
	}
	return nil
}

// ProposedByMe lists proposals this user has made for other people, so the
// author can see what is still waiting and what was declined.
func (s *Service) ProposedByMe(ctx context.Context) ([]domain.TimeEntry, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.db.ListEntriesEnteredBy(ctx, actor.ID, scope)
}
