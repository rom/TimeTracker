package service

import (
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
	// Hour is the hour of the day this rule marks, 0-23. The label is left to
	// the interface, because whether it reads "14:00" or "2pm" is an
	// administrator's choice and this package does not know about printers.
	Hour int
	// Slot is the quarter-hour row the label sits on.
	Slot int
}

// TimelineOutside counts entries that fall beyond the visible window, for the
// arrows that say so.
//
// Both a count and a total: "3 entries, 2h 30m" answers "is it worth looking?"
// where a bare arrow only says "something is there".
type TimelineOutside struct {
	Count   int
	Seconds int64
	// FirstHour is the hour the nearest one starts at, so the arrow can link
	// straight to it rather than making somebody hunt.
	FirstHour int
}

// Any reports whether there is anything out there at all.
func (o TimelineOutside) Any() bool { return o.Count > 0 }

// slotMinutes is the granularity of the timeline grid.
//
// Fifteen minutes: what people record in, what a drag snaps to, and small
// enough that a half-hour meeting is visibly half the height of an hour one.
const slotMinutes = 15

// buildTimeline places a day's entries on the grid.
//
// The visible range starts from the window an administrator configured. What
// happens to time recorded outside it is their choice too, and both answers are
// defensible:
//
//   - expand, the original behaviour, grows the pane until everything fits.
//     Right for somebody who works late once a month.
//   - arrows keeps the window fixed and reports what fell outside it. Right for
//     somebody whose evenings are routinely busy, whose ordinary working day
//     would otherwise be squeezed into the top third of the pane every day.
//
// Neither is correct for everyone, which is exactly why it is a setting rather
// than a decision made here.
func buildTimeline(view *DayView, window domain.DayWindow, overflow domain.DayOverflow, now time.Time) {
	loc := view.Location
	startHour, endHour := window.StartHour, window.EndHour

	if len(view.Entries) > 0 && overflow.OrDefault() == domain.DayOverflowExpand {
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
			Hour: hour % 24,
			Slot: (hour-startHour)*(60/slotMinutes) + 1,
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
		// Outside the window. With arrows chosen the block is not drawn at all
		// and is counted instead; the alternative - a sliver clamped to the top
		// edge - claims a start time the entry does not have, which is worse
		// than an honest "there is more up there".
		if slot+span <= 0 || slot >= view.Slots {
			outside := &view.Later
			if slot < 0 {
				outside = &view.Earlier
			}
			outside.Count++
			outside.Seconds += seconds
			if hour := start.Hour(); outside.Count == 1 || hour < outside.FirstHour {
				outside.FirstHour = hour
			}
			continue
		}
		if slot < 0 {
			// Straddling the top edge: the visible part is drawn, and the hour
			// it really began is still reported, because a block that appears
			// to start at eight when it started at six is a lie the pane would
			// be telling every morning.
			view.Earlier.Count++
			view.Earlier.Seconds += int64(-slot) * int64(slotMinutes) * 60
			if hour := start.Hour(); view.Earlier.Count == 1 || hour < view.Earlier.FirstHour {
				view.Earlier.FirstHour = hour
			}
			span += slot
			slot = 0
		}
		if slot+span > view.Slots {
			view.Later.Count++
			view.Later.Seconds += int64(slot+span-view.Slots) * int64(slotMinutes) * 60
			// The overflow begins where the window ends, by definition; there is
			// nothing nearer to point at.
			view.Later.FirstHour = endHour % 24
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
