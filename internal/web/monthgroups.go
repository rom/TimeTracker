package web

import (
	"time"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/i18n"
)

// Month headings down a listing of entries.
//
// The entries screen defaults to a range of weeks and can be widened to a year,
// so a page of fifty rows often crosses a month boundary - and a date column of
// fifty dates is something the eye slides off. A heading at each boundary gives
// the list the structure the dates imply but do not show.
//
// Only when it crosses one. A listing entirely inside October with "Entries in
// October 2026" written above it says nothing the heading of the screen did not,
// and a decoration that appears on every page is one nobody reads.

// entryGroup is one row of the listing: an entry, and the heading that precedes
// it when it is the first entry of a month.
type entryGroup struct {
	Entry domain.TimeEntry
	// Heading is empty unless this entry opens a month, and the listing spans
	// more than one.
	Heading string
	// Seconds is the month's counted total, rendered beside the heading. A
	// heading that only names the month makes somebody add the rows up
	// themselves, which is the question the heading invites.
	Seconds int64
	// Entries is how many rows the month has on this page.
	Entries int
}

// groupByMonth annotates a listing with a heading at each month boundary.
//
// The order is the listing's own - newest first by default - and the grouping
// follows it rather than imposing one: a filter sorted some other way would get
// headings wherever the month changed, which is still the truth about what is on
// screen.
//
// The totals and counts are per month *on this page*, not for the whole month:
// page two of a filtered search shows what page two contains, and claiming the
// month's real total beside a partial list would be a figure that does not add
// up to the rows under it. The heading says "in October" rather than "October
// total" for that reason.
func groupByMonth(entries []domain.TimeEntry, printer *i18n.Printer, loc *time.Location) []entryGroup {
	if len(entries) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}

	groups := make([]entryGroup, 0, len(entries))
	// A month is its calendar month in the entry's own zone, which is the zone
	// the date column is rendered in. Using the reader's would put a Stockholm
	// Monday evening under the previous month when read from New York on the
	// first of the month.
	monthOf := func(entry domain.TimeEntry) (int, time.Month) {
		local := entry.StartedAt.In(loc)
		return local.Year(), local.Month()
	}

	// Which months are present, and what each holds. Counted first so the
	// heading can carry the figures, and so a listing inside one month can be
	// left undecorated.
	type monthTotal struct {
		seconds int64
		entries int
	}
	totals := map[[2]int]*monthTotal{}
	var order [][2]int
	for _, entry := range entries {
		year, month := monthOf(entry)
		key := [2]int{year, int(month)}
		total, seen := totals[key]
		if !seen {
			total = &monthTotal{}
			totals[key] = total
			order = append(order, key)
		}
		total.entries++
		if entry.Counts() {
			total.seconds += entry.DurationSeconds
		}
	}

	spansMonths := len(order) > 1
	var current [2]int
	for _, entry := range entries {
		year, month := monthOf(entry)
		key := [2]int{year, int(month)}
		group := entryGroup{Entry: entry}
		if spansMonths && key != current {
			total := totals[key]
			group.Heading = printer.FormatMonth(time.Date(year, month, 1, 0, 0, 0, 0, loc))
			group.Seconds = total.seconds
			group.Entries = total.entries
			current = key
		}
		groups = append(groups, group)
	}
	return groups
}
