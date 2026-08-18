package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Moving time between assignments.
//
// The commonest correction anyone makes to a timesheet: the work was right, the
// assignment it was recorded against was wrong. Doing it one entry at a time
// through the edit form is tedious for a week's worth, so this moves a selection
// in one operation.
//
// Two things make it more than an UPDATE:
//
//   - the billing snapshot must be recomputed, because the new assignment may
//     carry a different rate and a different rounding rule. Leaving the old
//     figures would bill the new customer at the old customer's rate.
//   - every move is audited individually, with where it came from, because the
//     answer to "why did this project's hours change" has to be findable.

// MoveResult reports what a move did.
type MoveResult struct {
	Moved int
	// Skipped counts entries the actor could not move, so a partial result is
	// visible rather than silently smaller than expected.
	Skipped int
	// Target is where they went, for the confirmation message.
	Target domain.Assignment
}

// MoveEntries moves a selection of time entries to a different assignment.
//
// The target may belong to a different project and a different customer: that
// is precisely the correction people need, having recorded a day against the
// wrong client.
func (s *Service) MoveEntries(ctx context.Context, entryIDs []int64, targetAssignmentID int64) (MoveResult, error) {
	if len(entryIDs) == 0 {
		return MoveResult{}, fmt.Errorf("%w: select at least one entry to move", ErrValidation)
	}
	if targetAssignmentID == 0 {
		return MoveResult{}, fmt.Errorf("%w: choose where to move the time to", ErrValidation)
	}

	target, err := s.db.GetAssignment(ctx, targetAssignmentID)
	if err != nil {
		return MoveResult{}, err
	}
	if target.Archived() {
		return MoveResult{}, fmt.Errorf("%w: %q is archived", ErrValidation, target.Name)
	}

	// Permission to put work *into* the target, checked once.
	if err := s.authz.Can(ctx, auth.ActionCreate, auth.Resource{
		Type: "time_entry", ProjectID: target.ProjectID, CustomerID: target.CustomerID,
	}); err != nil {
		return MoveResult{}, err
	}

	result := MoveResult{Target: target}

	for _, entryID := range entryIDs {
		entry, err := s.db.GetEntry(ctx, entryID)
		if err != nil {
			result.Skipped++
			continue
		}
		// Permission to take work *out of* where it currently is, checked per
		// entry: a selection may span projects.
		if err := s.canModify(ctx, entry); err != nil {
			result.Skipped++
			continue
		}
		// A locked or already-decided proposal is not something to move
		// underneath its subject.
		if entry.Status == domain.StatusPending || entry.Status == domain.StatusRejected {
			result.Skipped++
			continue
		}

		moved := entry
		moved.AssignmentID = targetAssignmentID
		// Recompute from the target, not the source: the rate that applies is
		// the new assignment's.
		if err := s.applyBillingTo(ctx, &moved); err != nil {
			return result, err
		}

		fromLabel := entry.CustomerName + " / " + entry.ProjectName + " / " + entry.AssignmentName
		err = s.db.InTx(ctx, func(tx *sql.Tx) error {
			if _, txErr := tx.ExecContext(ctx, `
				UPDATE time_entries SET assignment_id = ?, rounding_rule_applied = ?,
				       billable_seconds = ?, rate_minor = ?, amount_minor = ?,
				       currency = ?, updated_at = ?
				WHERE id = ?`,
				targetAssignmentID, moved.RoundingRuleApplied, moved.BillableSeconds,
				moved.RateMinor, moved.AmountMinor, moved.Currency,
				s.now().UTC().Format(time.RFC3339), entryID); txErr != nil {
				return txErr
			}
			return s.audit(ctx, tx, "time_entry.move", "time_entry", entryID, 0, map[string]any{
				"from":         fromLabel,
				"to":           target.Label(),
				"amount_minor": map[string]any{"from": entry.AmountMinor, "to": moved.AmountMinor},
			})
		})
		if err != nil {
			return result, err
		}
		s.auditLog(ctx, "time_entry.move", "time_entry", entryID)
		result.Moved++
	}

	return result, nil
}

// MoveExpenses moves a selection of expenses to a different project.
func (s *Service) MoveExpenses(ctx context.Context, expenseIDs []int64, targetProjectID int64) (MoveResult, error) {
	if len(expenseIDs) == 0 {
		return MoveResult{}, fmt.Errorf("%w: select at least one expense to move", ErrValidation)
	}

	target, err := s.db.GetProject(ctx, targetProjectID)
	if err != nil {
		return MoveResult{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionCreate, auth.Resource{
		Type: "expense", ProjectID: target.ID, CustomerID: target.CustomerID,
	}); err != nil {
		return MoveResult{}, err
	}

	var result MoveResult
	for _, id := range expenseIDs {
		expense, err := s.db.GetExpense(ctx, id)
		if err != nil {
			result.Skipped++
			continue
		}
		if err := s.authz.Can(ctx, auth.ActionUpdate, auth.Resource{
			Type: "expense", ID: id, OwnerID: expense.UserID,
			ProjectID: expense.ProjectID, CustomerID: expense.CustomerID,
		}); err != nil {
			result.Skipped++
			continue
		}

		from := expense.CustomerName + " / " + expense.ProjectName
		expense.ProjectID = targetProjectID
		// The currency follows the customer, so moving between customers can
		// change it. The amount is a number of minor units in that currency;
		// re-denominating it would be inventing an exchange rate, so the figure
		// stays and the currency follows, which is what a person correcting a
		// mistake means.
		if target.Currency != "" {
			expense.Currency = target.Currency
		}
		expense.ApplyMarkup()

		if err := s.db.UpdateExpense(ctx, expense); err != nil {
			return result, err
		}
		if err := s.recordAudit(ctx, "expense.move", "expense", id, map[string]any{
			"from": from, "to": target.CustomerName + " / " + target.Name,
		}); err != nil {
			return result, err
		}
		result.Moved++
	}
	return result, nil
}

// SetFavourite marks or unmarks an assignment as a favourite.
//
// Favourites sort first in every picker and appear as one-click start buttons on
// the day view, so this is the fastest way to reshape someone's working set. It
// is a personal-feeling setting on a shared record, which is a known limitation:
// see the note in docs/DESIGN.md.
func (s *Service) SetFavourite(ctx context.Context, assignmentID int64, favourite bool) error {
	assignment, err := s.db.GetAssignment(ctx, assignmentID)
	if err != nil {
		return err
	}
	// Marking a favourite is a working preference rather than administration, so
	// it needs only the permission to use the assignment - otherwise a member
	// could not organise their own day.
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "assignment", ID: assignmentID,
		ProjectID: assignment.ProjectID, CustomerID: assignment.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}

	assignment.Favourite = favourite
	if err := s.db.UpdateAssignment(ctx, assignment); err != nil {
		return err
	}
	// Not audited: a favourite has no bearing on billing or access, and
	// auditing it would bury the events that matter.
	return nil
}
