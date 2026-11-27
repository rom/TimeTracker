package service

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Changing many entries at once.
//
// The property that matters is not that it is fast but that it is the same
// change, made the same way: a bulk path with its own SQL would be a second
// place for the authorisation, the week lock, the billing recomputation and
// the audit row to be got wrong, and it would be wrong quietly and in bulk.

// bulkFixture records four entries and returns their ids.
func bulkFixture(t *testing.T) (*fixture, []int64) {
	t.Helper()
	f := newFixture(t)

	var ids []int64
	for i := range 4 {
		entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID,
			StartedAt:    f.now.Add(time.Duration(i) * time.Hour),
			// Short, so four of them fit inside one day without overlapping
			// into anything the fixture cares about.
			DurationSeconds: 1800, Billable: true,
		})
		if err != nil {
			t.Fatalf("create entry %d: %v", i, err)
		}
		ids = append(ids, entry.ID)
	}
	return f, ids
}

// TestTaggingManyEntriesAtOnce.
func TestTaggingManyEntriesAtOnce(t *testing.T) {
	f, ids := bulkFixture(t)

	result, err := f.svc.BulkEntries(f.ctx, ids, BulkAddTag, "audit")
	if err != nil {
		t.Fatalf("add a tag: %v", err)
	}
	if result.Changed != len(ids) || len(result.Refused) != 0 {
		t.Fatalf("changed %d of %d, refused %v", result.Changed, len(ids), result.Refused)
	}
	for _, id := range ids {
		entry, err := f.svc.Entry(f.ctx, id)
		if err != nil {
			t.Fatalf("read %d: %v", id, err)
		}
		if len(entry.Tags) != 1 || entry.Tags[0] != "audit" {
			t.Errorf("entry %d carries %v, want the tag it was given", id, entry.Tags)
		}
	}

	// Adding it again changes nothing rather than carrying it twice.
	if _, err := f.svc.BulkEntries(f.ctx, ids, BulkAddTag, "audit"); err != nil {
		t.Fatalf("add it again: %v", err)
	}
	entry, err := f.svc.Entry(f.ctx, ids[0])
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(entry.Tags) != 1 {
		t.Errorf("adding the same tag twice left %v", entry.Tags)
	}

	// And taking it off takes it off.
	if _, err := f.svc.BulkEntries(f.ctx, ids, BulkRemoveTag, "audit"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if entry, err = f.svc.Entry(f.ctx, ids[0]); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(entry.Tags) != 0 {
		t.Errorf("the tag survived removal: %v", entry.Tags)
	}
}

// TestReassigningManyEntriesAtOnce.
func TestReassigningManyEntriesAtOnce(t *testing.T) {
	f, ids := bulkFixture(t)

	target, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create the target: %v", err)
	}

	result, err := f.svc.BulkEntries(f.ctx, ids, BulkReassign, itoa(target.ID))
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if result.Changed != len(ids) {
		t.Fatalf("changed %d of %d: %v", result.Changed, len(ids), result.Refused)
	}
	for _, id := range ids {
		entry, err := f.svc.Entry(f.ctx, id)
		if err != nil {
			t.Fatalf("read %d: %v", id, err)
		}
		if entry.AssignmentID != target.ID {
			t.Errorf("entry %d is on assignment %d, want %d",
				id, entry.AssignmentID, target.ID)
		}
	}
}

// TestAnArchivedTargetIsRefusedBeforeAnythingMoves.
//
// Validated once, up front. Discovering it on the fortieth entry would leave
// thirty-nine already moved somewhere nobody asked for.
func TestAnArchivedTargetIsRefusedBeforeAnythingMoves(t *testing.T) {
	f, ids := bulkFixture(t)

	retired, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Retired", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.SetAssignmentArchived(f.ctx, retired.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	result, err := f.svc.BulkEntries(f.ctx, ids, BulkReassign, itoa(retired.ID))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("moving onto an archived assignment = %v, want a refusal", err)
	}
	if result.Changed != 0 {
		t.Errorf("%d entries were moved before the refusal", result.Changed)
	}
	// Nothing moved.
	entry, err := f.svc.Entry(f.ctx, ids[0])
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if entry.AssignmentID != f.assignment.ID {
		t.Error("an entry moved despite the refusal")
	}
}

// TestALockedWeekRefusesItsOwnEntriesAndNotTheRest.
//
// Partial success, deliberately. A selection spanning one approved week and
// three open ones should change the three and say why the fourth was left:
// all-or-nothing would make one signed-off week block a repair to the rest,
// and the person would do it one at a time anyway.
func TestALockedWeekRefusesItsOwnEntriesAndNotTheRest(t *testing.T) {
	f, ids := bulkFixture(t)

	// One entry a fortnight earlier, in a week of its own, which is then
	// submitted and so locked.
	earlier := f.now.AddDate(0, 0, -14)
	locked, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: earlier,
		DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create the locked one: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, earlier); err != nil {
		t.Fatalf("submit: %v", err)
	}

	result, err := f.svc.BulkEntries(f.ctx, append(ids, locked.ID), BulkAddTag, "audit")
	if err != nil {
		t.Fatalf("bulk tag across a locked week = %v, want partial success", err)
	}
	if result.Changed != len(ids) {
		t.Errorf("changed %d, want the %d in open weeks", result.Changed, len(ids))
	}
	if len(result.Refused) != 1 {
		t.Fatalf("refused %d, want the one in the locked week: %v",
			len(result.Refused), result.Refused)
	}
	// The report says which one and why, because that is what somebody acts on.
	if result.Refused[0].EntryID != locked.ID {
		t.Errorf("refused entry %d, want %d", result.Refused[0].EntryID, locked.ID)
	}
	if !strings.Contains(result.Refused[0].Reason, "submitted") {
		t.Errorf("the reason does not say the week was submitted: %q",
			result.Refused[0].Reason)
	}
	if result.Refused[0].Label == "" {
		t.Error("the refusal names no entry, so the report cannot be acted on")
	}
}

// TestDeletingManyAtOnceIsStillRecoverable.
func TestDeletingManyAtOnceIsStillRecoverable(t *testing.T) {
	f, ids := bulkFixture(t)

	result, err := f.svc.BulkEntries(f.ctx, ids, BulkDelete, "")
	if err != nil {
		t.Fatalf("bulk delete: %v", err)
	}
	if result.Changed != len(ids) {
		t.Fatalf("deleted %d of %d: %v", result.Changed, len(ids), result.Refused)
	}

	bin, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil {
		t.Fatalf("list the bin: %v", err)
	}
	if len(bin) != len(ids) {
		t.Errorf("the bin holds %d of %d deleted entries; a bulk deletion goes "+
			"through the same path as a single one and is recoverable the same way",
			len(bin), len(ids))
	}
}

// TestABulkChangeRefusesAnEmptySelectionAndAnUnknownAction.
func TestABulkChangeRefusesAnEmptySelectionAndAnUnknownAction(t *testing.T) {
	f, ids := bulkFixture(t)

	if _, err := f.svc.BulkEntries(f.ctx, nil, BulkAddTag, "audit"); !errors.Is(err, ErrValidation) {
		t.Errorf("an empty selection = %v, want a refusal", err)
	}
	if _, err := f.svc.BulkEntries(f.ctx, ids, "nonsense", ""); !errors.Is(err, ErrValidation) {
		t.Errorf("an unknown action = %v, want a refusal", err)
	}
	if _, err := f.svc.BulkEntries(f.ctx, ids, BulkAddTag, ""); !errors.Is(err, ErrValidation) {
		t.Errorf("tagging with no tag = %v, want a refusal", err)
	}
}

// itoa passes an id the way the form does: as the text of a number.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }
