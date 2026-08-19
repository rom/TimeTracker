package service

import (
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The timeline's geometry.
//
// It is computed on the server so the day draws with no JavaScript at all, and
// it is expressed as grid slots rather than percentages because the content
// security policy forbids inline styles. Both of those make it ordinary Go with
// ordinary tests, which is most of the argument for doing it this way.

func TestTimelinePlacesBlocksOnTheGrid(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 2 * 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(day.Blocks))
	}

	// An hour of padding before the earliest entry, so the window starts at 08.
	if day.StartHour != 8 {
		t.Errorf("window starts at %02d, want 08", day.StartHour)
	}
	// 09:00 is four quarter-hours after 08:00, and slots are 1-based.
	if day.Blocks[0].Slot != 5 {
		t.Errorf("slot = %d, want 5 (09:00 in an 08:00 window)", day.Blocks[0].Slot)
	}
	if day.Blocks[0].Span != 8 {
		t.Errorf("span = %d, want 8 quarter-hours for two hours", day.Blocks[0].Span)
	}
	// The grid has to be exactly as many rows as the window.
	if want := (day.EndHour - day.StartHour) * 4; day.Slots != want {
		t.Errorf("slots = %d, want %d", day.Slots, want)
	}
	for _, hour := range day.Hours {
		if hour.Slot < 1 || hour.Slot > day.Slots {
			t.Errorf("hour %s sits on slot %d, outside the grid", hour.Label, hour.Slot)
		}
	}
}

// TestTimelineGivesOverlapsTheirOwnLane is the reason a timeline is worth
// drawing in an application that allows concurrent timers: two overlapping
// entries have to be visible as two, not one hiding the other.
func TestTimelineGivesOverlapsTheirOwnLane(t *testing.T) {
	f := newFixture(t)
	second, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: f.assignment.ProjectID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 09:00-12:00 and 10:30-12:30 overlap; 14:00 does not.
	for _, entry := range []struct {
		assignment    int64
		hour, minutes int
		seconds       int64
	}{
		{f.assignment.ID, 9, 0, 3 * 3600},
		{second.ID, 10, 30, 2 * 3600},
		{f.assignment.ID, 14, 0, 3600},
	} {
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID:    entry.assignment,
			StartedAt:       at(f.now, entry.hour, entry.minutes),
			DurationSeconds: entry.seconds, Billable: true,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(day.Blocks))
	}

	bySlot := map[int]TimelineBlock{}
	for _, block := range day.Blocks {
		bySlot[block.Slot] = block
	}
	// Two lanes, because two entries overlap.
	for _, block := range day.Blocks {
		if block.Lanes != 2 {
			t.Errorf("lanes = %d, want 2", block.Lanes)
		}
	}
	// The overlapping pair is in different lanes; the later, separate entry
	// reuses the first lane rather than opening a third.
	first := day.Blocks[0]
	overlapping := day.Blocks[1]
	if first.Lane == overlapping.Lane {
		t.Errorf("two overlapping entries share lane %d", first.Lane)
	}
	if day.Blocks[2].Lane != 1 {
		t.Errorf("a non-overlapping entry took lane %d rather than reusing the first",
			day.Blocks[2].Lane)
	}
}

// TestTimelineWindowCoversWhatIsRecorded: somebody who worked at six in the
// morning sees it, and somebody with an ordinary day does not get sixteen empty
// hours to scroll through.
func TestTimelineWindowCoversWhatIsRecorded(t *testing.T) {
	f := newFixture(t)

	empty, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if empty.StartHour != 8 || empty.EndHour != 18 {
		t.Errorf("an empty day spans %02d-%02d, want the working day", empty.StartHour, empty.EndHour)
	}

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 5, 30),
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 21, 0),
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	wide, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if wide.StartHour > 5 {
		t.Errorf("the window starts at %02d, missing the 05:30 entry", wide.StartHour)
	}
	if wide.EndHour < 22 {
		t.Errorf("the window ends at %02d, cutting off the 21:00 entry", wide.EndHour)
	}
	// Every block sits inside the grid. A block placed outside it would be
	// invisible, and the whole point of the view is that nothing is hidden.
	for _, block := range wide.Blocks {
		if block.Slot < 1 || block.Slot+block.Span-1 > wide.Slots {
			t.Errorf("a block occupies slots %d-%d, outside a grid of %d",
				block.Slot, block.Slot+block.Span-1, wide.Slots)
		}
	}
}

// TestTimelineClampsToTheDay. An entry running past midnight must not stretch
// the scale until everything else is a sliver.
func TestTimelineClampsToTheDay(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 22, 0),
		DurationSeconds: 6 * 3600, Billable: true, // runs to 04:00 the next day
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if day.EndHour > 24 {
		t.Errorf("the window ends at %02d", day.EndHour)
	}
	for _, block := range day.Blocks {
		if block.Slot+block.Span-1 > day.Slots {
			t.Errorf("a block overruns the grid: slots %d-%d of %d",
				block.Slot, block.Slot+block.Span-1, day.Slots)
		}
	}
}

// TestTimelineGivesAShortEntrySomethingToGrab. A block with no height cannot be
// seen or dragged, so a five-minute entry still gets a slot.
func TestTimelineGivesAShortEntrySomethingToGrab(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 9, 0),
		DurationSeconds: 120, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day.Blocks) != 1 || day.Blocks[0].Span < 1 {
		t.Fatalf("a two-minute entry got %+v", day.Blocks)
	}
}

// TestTimelineSlotClassesExistInTheStylesheet is the check that makes the
// grid-class approach safe.
//
// The server picks a class name; if the stylesheet has no rule for it the block
// renders at the top of the grid with no indication anything is wrong. The
// stylesheet generates rules up to a fixed maximum, and this proves the server
// cannot exceed it.
func TestTimelineSlotsStayWithinTheGeneratedRules(t *testing.T) {
	f := newFixture(t)

	// A day spanning midnight to midnight is the widest window possible, and
	// therefore the most slots.
	for _, hour := range []int{0, 23} {
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: at(f.now, hour, 0),
			DurationSeconds: 1800, Billable: true,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	// 24 hours at four slots an hour. The stylesheet generates slot- and span-
	// rules to 96 and lane rules to maxTimelineLanes.
	if day.Slots > 96 {
		t.Errorf("slots = %d, beyond the 96 the stylesheet generates", day.Slots)
	}
	if day.Slots%4 != 0 {
		t.Errorf("slots = %d, not a whole number of hours - no grid rule matches", day.Slots)
	}
	for _, block := range day.Blocks {
		if block.Slot > 96 || block.Span > 96 {
			t.Errorf("block at slot %d span %d, beyond the generated rules",
				block.Slot, block.Span)
		}
		if block.Lane > maxTimelineLanes {
			t.Errorf("block in lane %d, beyond the %d the stylesheet generates",
				block.Lane, maxTimelineLanes)
		}
	}
}

// TestTimelineHandlesManyOverlaps: more concurrent timers than there are lane
// rules must still produce placed blocks, ugly rather than invisible.
func TestTimelineHandlesManyOverlaps(t *testing.T) {
	f := newFixture(t)

	for i := 0; i < maxTimelineLanes+3; i++ {
		assignment, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
			ProjectID: f.assignment.ProjectID,
			Name:      "Concurrent " + time.Duration(i).String(),
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: assignment.ID, StartedAt: at(f.now, 9, 0),
			DurationSeconds: 3600, Billable: true,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day.Blocks) != maxTimelineLanes+3 {
		t.Fatalf("blocks = %d, want every entry", len(day.Blocks))
	}
	for _, block := range day.Blocks {
		if block.Lane < 1 || block.Lane > maxTimelineLanes {
			t.Errorf("lane = %d, outside the generated rules", block.Lane)
		}
	}
}
