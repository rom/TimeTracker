package service

import (
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Whether recorded time is chargeable, and who decides.
//
// An assignment carries billable_default, which is its answer for time
// recorded against it: the internal assignment somebody books Friday
// afternoon's admin to is deliberately not billable. The timer and quick-add
// have always honoured it. The manual form did not - it sent a checkbox that
// was ticked unconditionally in the markup - so the same hour recorded two
// ways got two answers, and the one the form gave put internal time into the
// Amount column of a client's export.
//
// These tests are about the service, which is where the decision belongs: the
// form can only say whether anybody expressed an opinion.

// nonBillable makes an assignment that says so.
func (f *fixture) nonBillable(t *testing.T) domain.Assignment {
	t.Helper()

	assignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Internal", BillableDefault: false,
	})
	if err != nil {
		t.Fatalf("create non-billable assignment: %v", err)
	}
	return assignment
}

func TestTimeFollowsItsAssignmentWhenNobodySaysOtherwise(t *testing.T) {
	f := newFixture(t)
	internal := f.nonBillable(t)

	for _, c := range []struct {
		name       string
		assignment domain.Assignment
		want       bool
	}{
		{"a billable assignment", f.assignment, true},
		{"an assignment that is not billable", internal, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
				AssignmentID:    c.assignment.ID,
				StartedAt:       f.now,
				DurationSeconds: 3600,
				Note:            "An hour",
				// What the add form sends: no opinion, and in particular not
				// the false that an unticked checkbox would arrive as.
				BillableFromAssignment: true,
			})
			if err != nil {
				t.Fatalf("create entry: %v", err)
			}
			if entry.Billable != c.want {
				t.Errorf("billable = %v, want %v: the assignment says %v and "+
					"nobody said otherwise", entry.Billable, c.want, c.want)
			}
		})
	}
}

func TestSomebodySayingSoBeatsTheAssignment(t *testing.T) {
	f := newFixture(t)
	internal := f.nonBillable(t)

	// Chargeable time on an assignment that is not normally chargeable. This
	// is the case the checkbox exists for, and it has to survive: a default is
	// not a rule.
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID:    internal.ID,
		StartedAt:       f.now,
		DurationSeconds: 3600,
		Note:            "Billed by agreement",
		Billable:        true,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if !entry.Billable {
		t.Error("an explicit yes was overruled by the assignment's default")
	}

	// And the other way: not chargeable on an assignment that normally is.
	entry, err = f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID:    f.assignment.ID,
		StartedAt:       f.now.Add(2 * time.Hour),
		DurationSeconds: 3600,
		Note:            "Our own mistake to fix",
		Billable:        false,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if entry.Billable {
		t.Error("an explicit no was overruled by the assignment's default")
	}
}

// The three ways to record an hour agree about it.
//
// This is the test the defect would have failed. The timer and quick-add read
// the assignment; the manual form says it has no opinion and the service reads
// the assignment for it. Any future fourth way that gets this wrong is a
// disagreement between rows in one timesheet.
func TestEveryWayOfRecordingAnHourAgreesAboutBilling(t *testing.T) {
	f := newFixture(t)
	internal := f.nonBillable(t)

	timer, err := f.svc.StartTimer(f.ctx, internal.ID, "Admin")
	if err != nil {
		t.Fatalf("start timer: %v", err)
	}
	f.advance(time.Hour)
	stopped, err := f.svc.StopTimer(f.ctx, timer.ID)
	if err != nil {
		t.Fatalf("stop timer: %v", err)
	}

	quick, parsed, err := f.svc.QuickAdd(f.ctx, "1h "+internal.Name+" admin")
	if err != nil {
		t.Fatalf("quick add: %v", err)
	}
	if parsed.Ambiguous {
		t.Fatalf("quick add could not resolve the assignment: %s", parsed.Reason)
	}

	manual, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID:           internal.ID,
		StartedAt:              f.now.Add(3 * time.Hour),
		DurationSeconds:        3600,
		Note:                   "Admin",
		BillableFromAssignment: true,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	for _, c := range []struct {
		how   string
		entry domain.TimeEntry
	}{
		{"the timer", stopped},
		{"quick add", quick},
		{"the manual form", manual},
	} {
		if c.entry.Billable {
			t.Errorf("%s recorded chargeable time against an assignment that "+
				"is not chargeable", c.how)
		}
	}
}
