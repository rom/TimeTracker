package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Changing many entries at once.
//
// Re-tagging or re-assigning a batch of misfiled time was one entry at a time,
// which on a week of mistakes - an importer pointed at the wrong assignment, a
// tag renamed in somebody's head but not in the data - is thirty round trips
// through a correction form.
//
// Every change goes through the single-entry path that already exists rather
// than through SQL of its own. That is slower, and it is the point: the
// authorisation, the week lock, the weekend policy, the billing recomputation
// and the audit row are the ones already written and tested, and a bulk path
// with its own query would be a second place for all five to be got wrong.

// BulkAction is what to do to the selected entries.
type BulkAction string

const (
	// BulkReassign moves entries to another assignment. The commonest repair:
	// an import pointed at the wrong one.
	BulkReassign BulkAction = "reassign"
	// BulkAddTag adds one tag to every selected entry, leaving the rest alone.
	BulkAddTag BulkAction = "add_tag"
	// BulkRemoveTag takes one tag off, and is a no-op on an entry not carrying
	// it rather than an error.
	BulkRemoveTag BulkAction = "remove_tag"
	// BulkDelete removes them. Recoverable, like any other deletion
	// (docs/adr/0037-deletion-is-recoverable.md).
	BulkDelete BulkAction = "delete"
)

// BulkRefusal is one entry that could not be changed, and why.
type BulkRefusal struct {
	EntryID int64
	// Label names the entry, so the report says which Tuesday was locked
	// rather than which row id.
	Label  string
	Reason string
}

// BulkResult reports what happened.
//
// Partial success is reported rather than refused. A selection spanning a
// locked week and nine open ones should change the nine and say so: all-or-
// nothing would make one approved week block a repair to the rest, and the
// person would do it one at a time anyway.
type BulkResult struct {
	Changed  int
	Refused  []BulkRefusal
	Action   BulkAction
	Selected int
}

// BulkEntries applies one change to many entries.
func (s *Service) BulkEntries(ctx context.Context, ids []int64, action BulkAction, value string) (BulkResult, error) {
	result := BulkResult{Action: action, Selected: len(ids)}
	if len(ids) == 0 {
		return result, fmt.Errorf("%w: nothing was selected", ErrValidation)
	}

	// Validated once, before anything changes, so a mistyped assignment does
	// not get discovered on the fortieth entry with thirty-nine already moved.
	var assignmentID int64
	switch action {
	case BulkReassign:
		assignmentID = int64Value(value)
		assignment, err := s.db.GetAssignment(ctx, assignmentID)
		if err != nil {
			return result, err
		}
		if assignment.Archived() {
			return result, fmt.Errorf("%w: %q has been archived",
				ErrValidation, assignment.Name)
		}
	case BulkAddTag, BulkRemoveTag:
		if value == "" {
			return result, fmt.Errorf("%w: no tag was given", ErrValidation)
		}
	case BulkDelete:
	default:
		return result, fmt.Errorf("%w: unknown bulk action %q", ErrValidation, action)
	}

	for _, id := range ids {
		entry, err := s.Entry(ctx, id)
		if err != nil {
			if !isRefusal(err) {
				return result, err
			}
			// Not readable means not this person's to change. Reported by id
			// alone, because naming a record somebody may not see would be the
			// leak the refusal exists to prevent.
			result.Refused = append(result.Refused, BulkRefusal{
				EntryID: id, Label: "", Reason: notFoundFor(err).Error(),
			})
			continue
		}
		label := entry.AssignmentName + " · " + entry.StartedAt.Format("2006-01-02")

		if action == BulkDelete {
			if err := s.DeleteEntry(ctx, id); err != nil {
				if !isRefusal(err) {
					return result, err
				}
				result.Refused = append(result.Refused,
					BulkRefusal{EntryID: id, Label: label, Reason: err.Error()})
				continue
			}
			result.Changed++
			continue
		}

		in := EntryInput{
			AssignmentID:    entry.AssignmentID,
			StartedAt:       entry.StartedAt,
			DurationSeconds: entry.DurationSeconds,
			Note:            entry.Note,
			Billable:        entry.Billable,
			Kind:            entry.Kind,
			Tags:            entry.Tags,
		}
		switch action {
		case BulkReassign:
			if entry.AssignmentID == assignmentID {
				// Already there. Counted as changed, because the person asked
				// for it to be on that assignment and it is.
				result.Changed++
				continue
			}
			in.AssignmentID = assignmentID
		case BulkAddTag:
			in.Tags = withTag(entry.Tags, value)
		case BulkRemoveTag:
			in.Tags = withoutTag(entry.Tags, value)
		}

		if _, err := s.UpdateEntry(ctx, id, in); err != nil {
			if !isRefusal(err) {
				return result, err
			}
			result.Refused = append(result.Refused,
				BulkRefusal{EntryID: id, Label: label, Reason: err.Error()})
			continue
		}
		result.Changed++
	}

	return result, nil
}

// withTag returns tags with one added, and unchanged if it is already there.
func withTag(tags []string, add string) []string {
	normalised := domain.NormaliseTag(add)
	for _, tag := range tags {
		if domain.NormaliseTag(tag) == normalised {
			return tags
		}
	}
	return append(append([]string{}, tags...), normalised)
}

// withoutTag returns tags with one removed, and unchanged if it was not there.
func withoutTag(tags []string, remove string) []string {
	normalised := domain.NormaliseTag(remove)
	kept := make([]string, 0, len(tags))
	for _, tag := range tags {
		if domain.NormaliseTag(tag) == normalised {
			continue
		}
		kept = append(kept, tag)
	}
	return kept
}

// int64Value reads a decimal id out of a form value, giving zero for anything
// that is not one - which the caller's lookup then refuses by name.
func int64Value(value string) int64 {
	var out int64
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil {
		return 0
	}
	return out
}

// isRefusal reports whether an error is a decision about one entry rather than
// a failure of the machinery.
//
// The distinction is what keeps partial success honest. A locked week, an
// entry belonging to somebody else, a weekend the instance does not work: each
// is an answer about that entry, and the other thirty-nine should still be
// changed. A closed database, a disk that will not write, a context that has
// been cancelled: none of those is an answer about anything, and collecting
// them as per-entry refusals would report "nothing changed, forty refused" to
// a caller with no way to tell that the database was simply gone.
//
// Found by the sweep that calls every service method against a closed
// database: this reported success against one, which is exactly the lie that
// test exists to catch.
func isRefusal(err error) bool {
	return errors.Is(err, ErrValidation) ||
		errors.Is(err, ErrForbidden) ||
		errors.Is(err, store.ErrNotFound)
}
