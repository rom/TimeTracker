package service

import (
	"fmt"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Laying a day out as blocks against a clock.
//
// A list answers "what did I record". A timeline answers "what does my day look
// like", which is the question somebody asks when they are hunting for the hour
// they have not accounted for - and overlaps become visible as overlaps rather
// than as a discrepancy between two totals.
//
// The geometry is computed here rather than in the browser for two reasons: it
// has to be right with no JavaScript at all, and "which blocks overlap" is the
// same interval arithmetic the elapsed-time total already does. Percentages
// rather than pixels, so the stylesheet decides how tall an hour is.

// TimelineBlock is one entry placed on the day's grid.
//
// Positions are grid slots rather than percentages because the content security
// policy forbids inline style attributes, and rightly: allowing them for
// geometry would allow them for everything. Slots are quarter hours, which is
// both the granularity people record in and the granularity a drag snaps to, so
// nothing is lost that anybody was expressing.
type TimelineBlock struct {
	Entry domain.TimeEntry
	// Slot is the quarter-hour row the block starts on, 1-based, and Span is
	// how many rows it covers.
	Slot int
	Span int
	// Lane and Lanes share the width between entries that overlap. Two
	// concurrent timers sit side by side rather than one hiding the other,
	// which is the whole reason a timeline is worth drawing here
	// (docs/adr/0004-concurrent-timers.md).
	Lane  int
	Lanes int
}

// TimelineHour is one labelled rule.
type TimelineHour struct {
	Label string
	// Slot is the quarter-hour row the label sits on.
	Slot int
}

// slotMinutes is the granularity of the timeline grid.
//
// Fifteen minutes: what people record in, what a drag snaps to, and small
// enough that a half-hour meeting is visibly half the height of an hour one.
const slotMinutes = 15

// buildTimeline places a day's entries on the grid.
//
// The visible range is the working day widened to cover whatever is actually
// recorded: somebody who worked until midnight sees it, and somebody with a
// normal day does not get sixteen empty hours of scrolling.
func buildTimeline(view *DayView, now time.Time) {
	loc := view.Location
	startHour, endHour := 8, 18

	if len(view.Entries) > 0 {
		first := view.Entries[0].StartedAt.In(loc)
		last := first
		for _, entry := range view.Entries {
			start := entry.StartedAt.In(loc)
			end := start.Add(time.Duration(entry.ElapsedSeconds(now)) * time.Second)
			if start.Before(first) {
				first = start
			}
			if end.After(last) {
				last = end
			}
		}

		// The working day, widened only where the entries fall outside it.
		//
		// Padding an hour beyond the *entries* rather than beyond the default
		// window: an ordinary day of work between nine and five should show
		// eight to six, not seven to seven with an empty hour at each end.
		// Somebody who worked at half past five in the morning sees it.
		if early := hourOf(first, view.Date, loc) - 1; early < startHour {
			startHour = early
		}
		late := hourOf(last, view.Date, loc) + 1
		if last.In(loc).Minute() > 0 || last.In(loc).Second() > 0 {
			late++
		}
		if late > endHour {
			endHour = late
		}

		// Clamped to the day: an entry running past midnight would otherwise
		// stretch the scale until everything else was a sliver.
		if startHour < 0 {
			startHour = 0
		}
		if endHour > 24 {
			endHour = 24
		}
		if endHour <= startHour {
			endHour = startHour + 1
		}
	}

	view.StartHour, view.EndHour = startHour, endHour
	view.Slots = (endHour - startHour) * (60 / slotMinutes)

	view.Hours = nil
	for hour := startHour; hour < endHour; hour++ {
		view.Hours = append(view.Hours, TimelineHour{
			Label: fmt.Sprintf("%02d:00", hour%24),
			Slot:  (hour-startHour)*(60/slotMinutes) + 1,
		})
	}
	if len(view.Entries) == 0 {
		view.Blocks = nil
		return
	}

	origin := view.Date.Add(time.Duration(startHour) * time.Hour)

	// Lanes: entries that overlap get adjacent columns. Greedy left to right,
	// which is what a calendar does and is right for the handful of concurrent
	// timers anybody actually runs.
	ordered := make([]domain.TimeEntry, len(view.Entries))
	copy(ordered, view.Entries)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].StartedAt.Before(ordered[j].StartedAt)
	})

	type placed struct {
		entry      domain.TimeEntry
		slot, span int
		lane       int
	}
	var blocks []placed
	laneEnds := []int{}

	for _, entry := range ordered {
		start := entry.StartedAt.In(loc)
		seconds := entry.ElapsedSeconds(now)
		if seconds <= 0 {
			// A just-started timer has no length yet. A block with no height
			// cannot be seen or grabbed, so it gets one slot.
			seconds = int64(slotMinutes) * 60
		}

		// Rounded rather than truncated, so an entry at 09:08 sits where a
		// reader would put it rather than a slot early.
		slot := int((start.Sub(origin).Minutes() + float64(slotMinutes)/2) / float64(slotMinutes))
		span := int((float64(seconds)/60 + float64(slotMinutes)/2) / float64(slotMinutes))
		if span < 1 {
			span = 1
		}
		if slot < 0 {
			span += slot
			slot = 0
		}
		if slot >= view.Slots {
			continue
		}
		if slot+span > view.Slots {
			span = view.Slots - slot
		}
		if span < 1 {
			span = 1
		}

		lane := -1
		for i, freeFrom := range laneEnds {
			if slot >= freeFrom {
				lane = i
				laneEnds[i] = slot + span
				break
			}
		}
		if lane < 0 {
			lane = len(laneEnds)
			laneEnds = append(laneEnds, slot+span)
		}
		blocks = append(blocks, placed{entry: entry, slot: slot + 1, span: span, lane: lane})
	}

	lanes := len(laneEnds)
	if lanes == 0 {
		lanes = 1
	}
	// The stylesheet carries lane rules up to a fixed number of columns. Beyond
	// it the extra timers share the last column, which is ugly and honest -
	// better than a block with no position at all.
	if lanes > maxTimelineLanes {
		lanes = maxTimelineLanes
	}

	view.Blocks = nil
	for _, block := range blocks {
		lane := block.lane
		if lane >= lanes {
			lane = lanes - 1
		}
		view.Blocks = append(view.Blocks, TimelineBlock{
			Entry: block.entry,
			Slot:  block.slot,
			Span:  block.span,
			Lane:  lane + 1,
			Lanes: lanes,
		})
	}
}

// maxTimelineLanes is how many overlapping entries get their own column.
//
// Six. Beyond that the columns are too narrow to read anyway, and the
// stylesheet would need a rule for every combination.
const maxTimelineLanes = 6

// hourOf returns an instant's hour on a day, clamped to it.
func hourOf(instant, day time.Time, loc *time.Location) int {
	local := instant.In(loc)
	if local.Before(day) {
		return 0
	}
	if local.Day() != day.Day() {
		return 24
	}
	return local.Hour()
}
