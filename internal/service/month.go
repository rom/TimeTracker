package service

import (
	"context"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// The month overview.
//
// The day screen is where time is entered and the week is what gets submitted.
// A month is neither: it is the view somebody opens to answer a different kind
// of question - was last month busier than this one, which days have nothing on
// them, did I actually work the Friday before the holiday. So it is a calendar
// rather than a list, with a figure in each square and the days with nothing on
// them visible as gaps rather than absent.
//
// It aggregates in Go over the month's entries, the way the week view does,
// rather than asking the database for per-day sums. A month of one person's
// work is a few hundred rows; the query that avoided loading them would be a
// second way of deciding what counts towards a total, and ASR-008 is easier to
// keep true with one.

// MonthView is a month drawn as a calendar, with what the month went on.
type MonthView struct {
	// Start is midnight local on the first of the month; End is midnight on the
	// first of the next, exclusive.
	Start    time.Time
	End      time.Time
	Location *time.Location
	// Weeks are the calendar's rows, each seven days long and starting on the
	// configured first day of the week. The first and last may carry days from
	// the neighbouring months, which are rendered as blanks.
	Weeks []MonthWeek
	// Weekdays are the seven days of the first row, in order, for the column
	// headings. Taken from a real week rather than from a list of names so the
	// headings follow the configured first day of the week without the template
	// having to know what it is.
	Weekdays []time.Time
	// DaysInMonth is the denominator of "eleven days have time on them".
	DaysInMonth int
	// Totals covers the month, on the same counting rule as every other total.
	Totals domain.Totals
	// ByAssignment is what the month went on, longest first. The answer to "where
	// did March go" is a short list, not forty rows.
	ByAssignment []MonthAssignment
	// DaysRecorded is how many of the month's days have anything on them, which
	// is the figure that makes a sparse month obvious at a glance.
	DaysRecorded int
	// BusiestDay is the day with the most time on it, and the zero time when the
	// month is empty.
	BusiestDay time.Time
}

// MonthWeek is one row of the calendar.
type MonthWeek struct {
	// Start is the row's first day, for a link to the week view. It is the real
	// week start even when the row begins in the previous month, because that is
	// the week the link should open.
	Start time.Time
	Days  []MonthDay
	// Seconds is the row's total, counting only days inside the month: a row's
	// figure has to add up to the squares above it.
	Seconds int64
	// Number is the ISO week number, which is what people call a week out loud
	// and what a payroll deadline is expressed in.
	Number int
}

// MonthDay is one square.
type MonthDay struct {
	Date time.Time
	// InMonth is false for the leading and trailing days that only exist to
	// square off the grid. They are rendered as blanks rather than omitted,
	// because a calendar with a ragged first row is harder to read than one with
	// two empty squares.
	InMonth bool
	Seconds int64
	// BillableSeconds is shown nowhere by default; it is here because a month
	// view is where somebody asks what share of it was billable.
	BillableSeconds int64
	Entries         int
	// Submitted and Approved say whether the week this day belongs to has been
	// decided, so the calendar can show at a glance which parts of the month are
	// already settled.
	Flagged bool
}

// MonthAssignment is one assignment's share of the month.
type MonthAssignment struct {
	Assignment domain.Assignment
	Seconds    int64
	// Percent is the share of the month's counted time, rounded to a whole
	// number. Computed here rather than in the template because a share is
	// arithmetic, and arithmetic in a template is arithmetic without a test.
	Percent int
}

// Month builds the overview for the month containing a date.
//
// The date is interpreted in the acting user's zone, like every other screen:
// the first of the month is their first, not the server's.
func (s *Service) Month(ctx context.Context, date time.Time) (MonthView, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return MonthView{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return MonthView{}, err
	}

	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return MonthView{}, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return MonthView{}, err
	}

	loc := locationFor(actor)
	local := date.In(loc)
	start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)

	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		UserID: actor.ID, From: start, To: end, Scope: scope,
	})
	if err != nil {
		return MonthView{}, err
	}
	entries = s.narrowEntries(ctx, entries)

	view := MonthView{
		Start: start, End: end, Location: loc,
		DaysInMonth: int(end.Sub(start).Hours() / 24),
	}

	// One square per day of the month, keyed by day number so an entry can be
	// placed without arithmetic on time zones.
	days := map[int]*MonthDay{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		days[day.Day()] = &MonthDay{Date: day, InMonth: true}
	}

	byAssignment := map[int64]*MonthAssignment{}
	for _, entry := range entries {
		local := entry.StartedAt.In(loc)
		square, ok := days[local.Day()]
		if !ok || local.Month() != start.Month() {
			// A daylight-saving shift, or an entry the filter's half-open bounds
			// let in at the edge. Counted in the totals below but not placed on a
			// square that is not its own.
			continue
		}
		square.Entries++
		if entry.Flagged {
			square.Flagged = true
		}
		if !entry.Counts() {
			continue
		}
		seconds := entry.ElapsedSeconds(s.now())
		square.Seconds += seconds
		square.BillableSeconds += entry.BillableSeconds

		assignment, ok := byAssignment[entry.AssignmentID]
		if !ok {
			assignment = &MonthAssignment{Assignment: domain.Assignment{
				ID: entry.AssignmentID, Name: entry.AssignmentName,
				ProjectName: entry.ProjectName, CustomerName: entry.CustomerName,
				ColourKey: entry.ColourKey, Icon: entry.Icon,
			}}
			byAssignment[entry.AssignmentID] = assignment
		}
		assignment.Seconds += seconds
	}

	view.Totals = s.totals(entries)

	// The grid: whole weeks, starting on the configured first day, from the week
	// containing the first of the month to the week containing the last.
	gridStart := startOfWeek(start, settings.WeekStart, loc)
	for rowStart := gridStart; rowStart.Before(end); rowStart = rowStart.AddDate(0, 0, 7) {
		_, week := rowStart.ISOWeek()
		row := MonthWeek{Start: rowStart, Number: week}
		for i := 0; i < 7; i++ {
			day := rowStart.AddDate(0, 0, i)
			if day.Month() == start.Month() && day.Year() == start.Year() {
				square := days[day.Day()]
				row.Days = append(row.Days, *square)
				row.Seconds += square.Seconds
				continue
			}
			row.Days = append(row.Days, MonthDay{Date: day})
		}
		view.Weeks = append(view.Weeks, row)
	}
	if len(view.Weeks) > 0 {
		for _, day := range view.Weeks[0].Days {
			view.Weekdays = append(view.Weekdays, day.Date)
		}
	}

	for _, square := range days {
		if square.Seconds > 0 || square.Entries > 0 {
			view.DaysRecorded++
		}
	}
	busiest := int64(0)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		if square := days[day.Day()]; square.Seconds > busiest {
			busiest = square.Seconds
			view.BusiestDay = square.Date
		}
	}

	for _, assignment := range byAssignment {
		share := *assignment
		if view.Totals.SummedSeconds > 0 {
			// Half away from zero, in integers: a share of 49.6% reads as 50,
			// and the figures add up to 100 as closely as rounding allows.
			share.Percent = int((share.Seconds*200 + view.Totals.SummedSeconds) /
				(view.Totals.SummedSeconds * 2))
		}
		view.ByAssignment = append(view.ByAssignment, share)
	}
	// Longest first, and by label when two are equal so the order is stable
	// between requests - a list that reshuffles on reload is one nobody trusts.
	sort.Slice(view.ByAssignment, func(a, b int) bool {
		if view.ByAssignment[a].Seconds != view.ByAssignment[b].Seconds {
			return view.ByAssignment[a].Seconds > view.ByAssignment[b].Seconds
		}
		return view.ByAssignment[a].Assignment.Label() < view.ByAssignment[b].Assignment.Label()
	})

	return view, nil
}

// PreviousMonth and NextMonth are the neighbouring months' first days.
//
// Computed here rather than in a template, because "a month before the 31st of
// March" is a question with a wrong answer: Go's AddDate normalises the 31st of
// February into the 3rd of March, which would make the previous-month link skip
// February entirely. Anchoring on the first of the month avoids it.
func (v MonthView) PreviousMonth() time.Time { return v.Start.AddDate(0, -1, 0) }

// NextMonth is the first day of the month after this one.
func (v MonthView) NextMonth() time.Time { return v.Start.AddDate(0, 1, 0) }
