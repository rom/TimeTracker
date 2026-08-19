package service

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// DayView is everything the Today screen needs for one calendar day.
type DayView struct {
	// Date is midnight local time on the day being shown.
	Date     time.Time
	Location *time.Location
	Entries  []domain.TimeEntry
	Running  []domain.TimeEntry
	Totals   domain.Totals
	// Gaps are stretches of the working day with no entry covering them. They
	// are shown as prompts ("2h 15m unaccounted"), never filled in automatically.
	Gaps []Gap

	// The day drawn as blocks against a clock. Computed on the server so the
	// timeline is right with no JavaScript at all; see timeline.go.
	Blocks    []TimelineBlock
	Hours     []TimelineHour
	StartHour int
	EndHour   int
	// Slots is how many quarter-hour rows the grid has.
	Slots int
	// Earlier and Later describe time that falls outside the visible window,
	// for the arrows that say so. Both are empty when the pane was allowed to
	// expand, because then there is nothing outside it.
	Earlier TimelineOutside
	Later   TimelineOutside
}

// Gap is an uncovered stretch between the first and last entry of a day.
type Gap struct {
	From    time.Time
	To      time.Time
	Seconds int64
}

// WeekView is the weekly timesheet grid: assignments down, days across.
type WeekView struct {
	Start    time.Time // midnight local on the first day of the week
	End      time.Time // exclusive
	Location *time.Location
	Days     []time.Time
	Rows     []WeekRow
	// DayTotals is indexed in step with Days.
	DayTotals []domain.Totals
	Totals    domain.Totals
}

// WeekRow is one assignment's line across the week.
type WeekRow struct {
	Assignment domain.Assignment
	// Seconds is indexed in step with WeekView.Days.
	Seconds []int64
	Total   int64
}

// Day builds the Today view for a date in the acting user's time zone.
func (s *Service) Day(ctx context.Context, date time.Time) (DayView, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return DayView{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return DayView{}, err
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return DayView{}, err
	}

	loc := locationFor(actor)
	// The day boundary is local, not UTC: "Monday" means the user's Monday.
	// See docs/adr/0015-utc-storage-local-display.md.
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)

	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		UserID: actor.ID, From: start, To: end, Scope: scope,
	})
	if err != nil {
		return DayView{}, err
	}

	view := DayView{Date: start, Location: loc, Entries: entries}
	for _, e := range entries {
		if e.Running() {
			view.Running = append(view.Running, e)
		}
	}
	view.Totals = s.totals(entries)
	view.Gaps = findGaps(entries, s.now())

	// The pane's window and its overflow behaviour are instance settings. A
	// failure to read them is not worth failing the whole day screen over, so
	// the zero value falls through to the built-in default.
	settings, _ := s.db.GetSettings(ctx)
	buildTimeline(&view,
		domain.NormaliseDayWindow(settings.DayStartHour, settings.DayEndHour),
		domain.DayOverflow(settings.DayOverflow), s.now())
	return view, nil
}

// Week builds the weekly grid containing the given date.
func (s *Service) Week(ctx context.Context, date time.Time) (WeekView, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return WeekView{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return WeekView{}, err
	}

	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return WeekView{}, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return WeekView{}, err
	}

	loc := locationFor(actor)
	start := startOfWeek(date.In(loc), settings.WeekStart, loc)
	end := start.AddDate(0, 0, 7)

	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		UserID: actor.ID, From: start, To: end, Scope: scope,
	})
	if err != nil {
		return WeekView{}, err
	}

	view := WeekView{Start: start, End: end, Location: loc}
	for i := 0; i < 7; i++ {
		view.Days = append(view.Days, start.AddDate(0, 0, i))
	}
	view.DayTotals = make([]domain.Totals, 7)

	// Group by assignment, keeping one row per assignment with a cell per day.
	rowsByAssignment := map[int64]*WeekRow{}
	entriesByDay := make([][]domain.TimeEntry, 7)

	for _, e := range entries {
		dayIndex := int(e.StartedAt.In(loc).Sub(start).Hours() / 24)
		if dayIndex < 0 || dayIndex > 6 {
			// Defensive: a daylight-saving shift can make the arithmetic land
			// just outside the range. Clamp rather than drop the entry.
			if dayIndex < 0 {
				dayIndex = 0
			} else {
				dayIndex = 6
			}
		}
		entriesByDay[dayIndex] = append(entriesByDay[dayIndex], e)

		row, ok := rowsByAssignment[e.AssignmentID]
		if !ok {
			row = &WeekRow{
				Assignment: domain.Assignment{
					ID: e.AssignmentID, Name: e.AssignmentName,
					ProjectName: e.ProjectName, CustomerName: e.CustomerName,
					ColourKey: e.ColourKey, Icon: e.Icon,
				},
				Seconds: make([]int64, 7),
			}
			rowsByAssignment[e.AssignmentID] = row
		}
		if e.Counts() {
			seconds := e.ElapsedSeconds(s.now())
			row.Seconds[dayIndex] += seconds
			row.Total += seconds
		}
	}

	for i, dayEntries := range entriesByDay {
		view.DayTotals[i] = s.totals(dayEntries)
	}
	view.Totals = s.totals(entries)

	for _, row := range rowsByAssignment {
		view.Rows = append(view.Rows, *row)
	}
	// A stable order matters: the grid is read across weeks, and rows jumping
	// around between page loads makes it unreadable.
	sort.Slice(view.Rows, func(i, j int) bool {
		if view.Rows[i].Assignment.CustomerName != view.Rows[j].Assignment.CustomerName {
			return view.Rows[i].Assignment.CustomerName < view.Rows[j].Assignment.CustomerName
		}
		return view.Rows[i].Assignment.Label() < view.Rows[j].Assignment.Label()
	})
	return view, nil
}

// EntryFilter describes a query over entries.
//
// It is defined here rather than reused from the store so that the HTTP layer
// never names a storage type: a handler that could construct a store filter is
// one step from constructing a store query, which is the layering rule in
// docs/adr/0012-layered-package-structure.md.
type EntryFilter struct {
	// From and To bound the period; To is exclusive.
	From         time.Time
	To           time.Time
	CustomerID   int64
	ProjectID    int64
	AssignmentID int64
	BillableOnly bool
	Limit        int
	// UserID narrows to one person. Zero means "the acting user"; a manager or
	// administrator may name someone else, and the scope still applies on top.
	UserID int64
	// Tags narrows to entries carrying all of them.
	Tags []string
	// Query is free text over the note, the assignment, the project, the
	// customer and the tags. UseRegexp treats it as a regular expression.
	Query     string
	UseRegexp bool
	// Kinds limits to work, overtime or travel.
	Kinds []domain.EntryKind
	// Offset skips rows before the page being shown. Only meaningful with a
	// Limit, and deliberately absent from every export: a download covers what
	// the filter matches, not the page somebody happened to be looking at.
	Offset int
	// WithExpenses narrows to entries recorded on the same project and the same
	// calendar day as an expense - the day somebody drove to the customer, or
	// took them to lunch. Expenses hang off a project and a date rather than off
	// an entry, so this is the closest true statement to "time that had costs
	// attached to it", and the screen says as much next to the control.
	WithExpenses bool
}

// Entries lists entries matching a filter, for the Entries screen and as the
// basis of every export.
func (s *Service) Entries(ctx context.Context, filter EntryFilter) ([]domain.TimeEntry, error) {
	// Whose entries: your own unless you asked for someone else's and the
	// authoriser agrees. The scope applies on top regardless, so even an
	// approved cross-user read stays inside the actor's projects.
	subjectID, scope, err := s.entryAudience(ctx, filter)
	if err != nil {
		return nil, err
	}
	entries, _, err := s.searchEntries(ctx, subjectID, scope, filter)
	return entries, err
}

// EntryPage is one screenful of entries and how many there are in total.
type EntryPage struct {
	Entries []domain.TimeEntry
	// Total is how many the filter matches, not how many are in Entries. It is
	// what lets a screen say which page of how many it is showing, and what
	// tells somebody their filter matched more than they can see.
	Total int
	Mode  store.SearchMode
}

// SearchEntriesPage is SearchEntries for a screen that shows one page at a time.
func (s *Service) SearchEntriesPage(ctx context.Context, filter EntryFilter) (EntryPage, error) {
	subjectID, scope, err := s.entryAudience(ctx, filter)
	if err != nil {
		return EntryPage{}, err
	}
	entries, mode, total, err := s.searchPage(ctx, subjectID, scope, filter)
	if err != nil {
		return EntryPage{}, err
	}
	return EntryPage{Entries: entries, Total: total, Mode: mode}, nil
}

// entryAudience settles whose entries are being asked for and what the actor may
// see, which is the same question for every entry query and was answered three
// times over before this existed.
func (s *Service) entryAudience(ctx context.Context, filter EntryFilter) (int64, store.Scope, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return 0, store.Scope{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
	}); err != nil {
		return 0, store.Scope{}, err
	}

	subjectID := actor.ID
	if filter.UserID != 0 && filter.UserID != actor.ID {
		if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
			Type: "time_entry", OwnerID: filter.UserID, ProjectID: filter.ProjectID,
			CustomerID: filter.CustomerID,
		}); err != nil {
			return 0, store.Scope{}, notFoundFor(err)
		}
		subjectID = filter.UserID
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return 0, store.Scope{}, err
	}
	return subjectID, scope, nil
}

// SearchEntries is Entries with the search mechanism reported back, so the
// screen can say whether it matched an index, a scan or a regular expression.
func (s *Service) SearchEntries(ctx context.Context, filter EntryFilter) ([]domain.TimeEntry, store.SearchMode, error) {
	subjectID, scope, err := s.entryAudience(ctx, filter)
	if err != nil {
		return nil, store.SearchNone, err
	}
	return s.searchEntries(ctx, subjectID, scope, filter)
}

// storeFilter translates a service filter into the store's.
func storeFilter(subjectID int64, scope store.Scope, filter EntryFilter) store.EntryFilter {
	return store.EntryFilter{
		UserID:       subjectID,
		Scope:        scope,
		From:         filter.From,
		To:           filter.To,
		CustomerID:   filter.CustomerID,
		ProjectID:    filter.ProjectID,
		AssignmentID: filter.AssignmentID,
		BillableOnly: filter.BillableOnly,
		Tags:         filter.Tags,
		Query:        filter.Query,
		UseRegexp:    filter.UseRegexp,
		Kinds:        filter.Kinds,
		Limit:        filter.Limit,
		Offset:       filter.Offset,
	}
}

// searchEntries is the shared query, after the permission questions are settled.
func (s *Service) searchEntries(ctx context.Context, subjectID int64, scope store.Scope, filter EntryFilter) ([]domain.TimeEntry, store.SearchMode, error) {
	if filter.WithExpenses {
		entries, mode, _, err := s.searchWithExpenses(ctx, subjectID, scope, filter)
		return entries, mode, err
	}
	return s.db.SearchEntries(ctx, storeFilter(subjectID, scope, filter))
}

// searchPage is searchEntries with the total behind the page.
//
// The total is what lets a screen say "page 3 of 12" and what tells somebody
// their filter matched more than they can see. It is a second query rather than
// the length of the slice, because the length of a page is not the size of the
// result.
func (s *Service) searchPage(ctx context.Context, subjectID int64, scope store.Scope, filter EntryFilter) ([]domain.TimeEntry, store.SearchMode, int, error) {
	if filter.WithExpenses {
		return s.searchWithExpenses(ctx, subjectID, scope, filter)
	}

	entries, mode, err := s.db.SearchEntries(ctx, storeFilter(subjectID, scope, filter))
	if err != nil {
		return nil, store.SearchNone, 0, err
	}

	// Only when a page was asked for. An unbounded query already knows its own
	// size, and counting it again would double the work of every export.
	total := len(entries)
	if filter.Limit > 0 {
		windowless := filter
		windowless.Limit, windowless.Offset = 0, 0
		if total, err = s.db.CountEntries(ctx, storeFilter(subjectID, scope, windowless)); err != nil {
			return nil, store.SearchNone, 0, err
		}
	}
	return entries, mode, total, nil
}

// searchWithExpenses answers the "time on a day its project also had a cost"
// filter, and pages it in Go.
//
// The condition cannot be expressed in SQL - an entry's calendar day comes from
// the zone it was recorded in, and SQLite cannot convert an instant to a named
// zone's local date - so the rows are narrowed after the query. That means the
// database cannot count them either, and a LIMIT applied before the narrowing
// would return a page with holes in it. So the window is applied here, to the
// narrowed set, which is small by construction: it is only the days somebody
// actually spent money.
//
// The database query is still bounded, by a cap far above any plausible answer,
// so a filter that matches everything cannot pull a whole table into memory.
func (s *Service) searchWithExpenses(ctx context.Context, subjectID int64, scope store.Scope, filter EntryFilter) ([]domain.TimeEntry, store.SearchMode, int, error) {
	unwindowed := filter
	unwindowed.Limit, unwindowed.Offset = maxPostFilterRows, 0

	entries, mode, err := s.db.SearchEntries(ctx, storeFilter(subjectID, scope, unwindowed))
	if err != nil {
		return nil, store.SearchNone, 0, err
	}

	kept, err := s.keepEntriesWithExpenses(ctx, entries, filter)
	if err != nil {
		return nil, store.SearchNone, 0, err
	}
	total := len(kept)

	// The mode is the one the query used. Narrowing the result afterwards does
	// not change which mechanism answered, and reporting SearchNone here would
	// make the screen stop explaining a regexp search the moment somebody also
	// ticked this box.
	return pageOf(kept, filter.Offset, filter.Limit), mode, total, nil
}

// maxPostFilterRows bounds a query whose narrowing happens outside SQL.
//
// Ten thousand: far above any range somebody filters by expenses over, and far
// below anything that would trouble memory. A cap rather than no cap, because a
// filter the database cannot apply must not be a way to ask for everything.
const maxPostFilterRows = 10_000

// pageOf slices a result to a window, tolerating an offset past the end.
func pageOf(entries []domain.TimeEntry, offset, limit int) []domain.TimeEntry {
	if offset >= len(entries) {
		return nil
	}
	if offset > 0 {
		entries = entries[offset:]
	}
	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}
	return entries
}

// keepEntriesWithExpenses drops entries with no expense on the same project and
// day.
//
// Deliberately not a SQL condition. An entry's calendar day comes from the zone
// it was recorded in, which is a column, and SQLite cannot convert an instant to
// a named zone's local date - date(started_at) would give the UTC day and put
// an evening entry in Stockholm on the wrong side of midnight. The comparison
// therefore happens here, where domain.LocalDay already gets it right, over a
// result set the screen has already capped.
func (s *Service) keepEntriesWithExpenses(ctx context.Context, entries []domain.TimeEntry, filter EntryFilter) ([]domain.TimeEntry, error) {
	if len(entries) == 0 {
		return entries, nil
	}

	expenses, err := s.Expenses(ctx, ExpenseFilter{
		From: filter.From, To: filter.To,
		CustomerID: filter.CustomerID, ProjectID: filter.ProjectID,
	})
	if err != nil {
		return nil, err
	}

	spent := make(map[string]bool, len(expenses))
	for _, expense := range expenses {
		spent[projectDayKey(expense.ProjectID, expense.SpentOn)] = true
	}

	kept := entries[:0:0]
	for _, entry := range entries {
		if spent[projectDayKey(entry.ProjectID, entry.LocalDay().Format("2006-01-02"))] {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

// projectDayKey identifies one project on one calendar day.
func projectDayKey(projectID int64, day string) string {
	return strconv.FormatInt(projectID, 10) + "\x00" + day
}

// Totals computes the summed and elapsed figures for a set of entries.
func (s *Service) Totals(entries []domain.TimeEntry) domain.Totals {
	return s.totals(entries)
}

// totals is the one place the two totals are computed.
//
// Summed and elapsed are reported together everywhere, because overlapping
// timers make them differ: a 10-hour sum across an 8-hour window is honest
// parallel work, and showing only one of the numbers makes it look like either an
// error or under-billing. See docs/adr/0004-concurrent-timers.md.
func (s *Service) totals(entries []domain.TimeEntry) domain.Totals {
	now := s.now()
	var t domain.Totals
	intervals := make([]domain.Interval, 0, len(entries))

	for _, e := range entries {
		if !e.Counts() {
			// Pending proxy proposals and flagged entries are excluded from every
			// total until a human resolves them. This is the single place that
			// rule is applied for aggregation.
			continue
		}
		seconds := e.ElapsedSeconds(now)
		t.SummedSeconds += seconds
		if e.Billable {
			t.BillableSeconds += seconds
		}
		start := e.StartedAt.Unix()
		intervals = append(intervals, domain.Interval{Start: start, End: start + seconds})
	}

	t.ElapsedSeconds = domain.UnionSeconds(intervals)
	t.OverlapSeconds = t.SummedSeconds - t.ElapsedSeconds
	return t
}

// findGaps returns the uncovered stretches between the first and last entry of a
// day.
//
// Only the interior is reported: time before the first entry and after the last
// is not a gap, it is simply the part of the day that had not started or had
// finished. Very short gaps are ignored, since nobody wants to be prompted about
// the ninety seconds between stopping one timer and starting the next.
func findGaps(entries []domain.TimeEntry, now time.Time) []Gap {
	const minimumGapSeconds = 15 * 60

	intervals := make([]domain.Interval, 0, len(entries))
	for _, e := range entries {
		if !e.Counts() {
			continue
		}
		start := e.StartedAt.Unix()
		intervals = append(intervals, domain.Interval{
			Start: start, End: start + e.ElapsedSeconds(now),
		})
	}
	if len(intervals) < 2 {
		return nil
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].Start < intervals[j].Start })

	var gaps []Gap
	cursor := intervals[0].End
	for _, iv := range intervals[1:] {
		if iv.Start > cursor {
			if seconds := iv.Start - cursor; seconds >= minimumGapSeconds {
				gaps = append(gaps, Gap{
					From:    time.Unix(cursor, 0),
					To:      time.Unix(iv.Start, 0),
					Seconds: seconds,
				})
			}
		}
		if iv.End > cursor {
			cursor = iv.End
		}
	}
	return gaps
}

// startOfWeek returns midnight on the first day of the week containing t.
// weekStart is an ISO weekday number, 1 = Monday.
func startOfWeek(t time.Time, weekStart int, loc *time.Location) time.Time {
	if weekStart < 1 || weekStart > 7 {
		weekStart = 1
	}
	// Go's Sunday is 0; ISO's Sunday is 7.
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	offset := weekday - weekStart
	if offset < 0 {
		offset += 7
	}
	day := t.AddDate(0, 0, -offset)
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// locationFor resolves a user's time zone, falling back to UTC. The zone database
// is embedded in the binary so this works on a stock Windows machine, which has
// no system zoneinfo.
func locationFor(u domain.User) *time.Location {
	if u.TimeZone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(u.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}
