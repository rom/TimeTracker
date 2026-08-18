package service

import (
	"context"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Approval status, per person, per week.
//
// The queue answers "what is waiting for me". This answers the question a
// manager asks at the end of a month, which is a different one: who has *not*
// submitted. That is the absence of a row rather than a row in a particular
// state, which is why this cannot be built from the queue - the interesting
// cells are the empty ones.

// ApprovalStatus is what one cell of the grid shows.
type ApprovalStatus string

const (
	// StatusNothing is a week with no time recorded and nothing submitted.
	// Blank on screen: an empty cell for a week somebody did not work is not a
	// problem, and marking it would bury the cells that are.
	StatusNothing ApprovalStatus = ""
	// StatusNotSubmitted is time recorded with no submission. The cell the
	// report exists to surface.
	StatusNotSubmitted ApprovalStatus = "not_submitted"
	// StatusEmptySubmission is a submitted or decided week with no time in it,
	// which a restore or a deletion after submission can produce.
	StatusEmptySubmission ApprovalStatus = "empty_submission"
)

// ApprovalCell is one person's one week.
type ApprovalCell struct {
	WeekStart string
	// Status is the period's own status when there is one, otherwise one of the
	// derived statuses above. Kept as a plain string so the template has one
	// thing to switch on rather than two.
	Status string
	// Seconds is what is recorded in the week now; SubmittedSeconds is what was
	// recorded when it was submitted.
	Seconds          int64
	SubmittedSeconds int64
	// Changed marks a week whose total has moved since submission. It should
	// not happen - a submitted week is locked - so when it does, it matters.
	Changed bool
	Note    string
}

// Recorded reports whether there is any time in the week.
func (c ApprovalCell) Recorded() bool { return c.Seconds > 0 }

// ApprovalRow is one person across the reported weeks.
type ApprovalRow struct {
	UserID   int64
	UserName string
	// Cells is aligned with ApprovalReport.Weeks, one per week, always the same
	// length - a template renders a grid and a short row would misalign it.
	Cells []ApprovalCell
	// TotalSeconds and Outstanding summarise the row, so a long grid can be
	// scanned by its right-hand edge.
	TotalSeconds int64
	Outstanding  int
}

// ApprovalWeek is one column's summary.
type ApprovalWeek struct {
	WeekStart string
	// Counts by status, for the column footer.
	Submitted    int
	Approved     int
	Rejected     int
	NotSubmitted int
	TotalSeconds int64
}

// ApprovalReport is the whole grid.
type ApprovalReport struct {
	From  time.Time
	To    time.Time
	Weeks []ApprovalWeek
	Rows  []ApprovalRow
	// Outstanding is how many person-weeks have time recorded but nothing
	// submitted, across the whole report. The number a manager is chasing.
	Outstanding int
	// TotalSeconds is every hour in the grid.
	TotalSeconds int64
}

// ApprovalStatuses are the derived statuses, for the key beneath the grid. The
// period's own statuses come from the domain.
func ApprovalStatuses() []string {
	return []string{
		string(domain.PeriodSubmitted),
		string(domain.PeriodApproved),
		string(domain.PeriodRejected),
		string(StatusNotSubmitted),
		string(StatusEmptySubmission),
	}
}

// ApprovalReportFor builds the grid for the weeks ending at `date`.
//
// weeks is how many to show, counting back from the week containing date.
func (s *Service) ApprovalReportFor(ctx context.Context, date time.Time, weeks int) (ApprovalReport, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return ApprovalReport{}, err
	}
	// Seeing other people's submission status is a management question, so it
	// takes the same permission approving does. Without it the report is the
	// actor's own weeks, which is still useful and gives away nothing.
	canManage := s.authz.Can(ctx, auth.ActionApprove, auth.Resource{Type: "timesheet"}) == nil

	if weeks <= 0 {
		weeks = 12
	}
	if weeks > 53 {
		// A year is the most this shape is readable at, and an unbounded value
		// from a query string should not become an unbounded query.
		weeks = 53
	}

	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return ApprovalReport{}, err
	}
	loc := locationFor(actor)

	// The columns: `weeks` week-starts ending with the one containing date.
	lastStart, err := domain.ParseWeekStart(
		domain.WeekStartFor(date, settings.WeekStart, loc), loc)
	if err != nil {
		return ApprovalReport{}, err
	}
	firstStart := lastStart.AddDate(0, 0, -7*(weeks-1))

	report := ApprovalReport{From: firstStart, To: domain.WeekEnd(lastStart)}
	columnAt := make(map[string]int, weeks)
	for i := 0; i < weeks; i++ {
		start := firstStart.AddDate(0, 0, 7*i)
		key := start.Format("2006-01-02")
		columnAt[key] = i
		report.Weeks = append(report.Weeks, ApprovalWeek{WeekStart: key})
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return ApprovalReport{}, err
	}

	entryFilter := store.EntryFilter{
		From:  firstStart,
		To:    report.To,
		Scope: scope,
	}
	periodFrom, periodTo := report.Weeks[0].WeekStart, report.To.Format("2006-01-02")
	var periods []domain.TimesheetPeriod
	if canManage {
		if periods, err = s.db.ListPeriodsInRange(ctx, periodFrom, periodTo, scope); err != nil {
			return ApprovalReport{}, err
		}
	} else {
		entryFilter.UserID = actor.ID
		if periods, err = s.db.ListPeriodsForUser(ctx, actor.ID, weeks); err != nil {
			return ApprovalReport{}, err
		}
	}

	entries, err := s.db.ListEntries(ctx, entryFilter)
	if err != nil {
		return ApprovalReport{}, err
	}

	// Rows are built from whoever appears at all - in the entries or in the
	// periods. Listing every account instead would fill the grid with people
	// who do not report time, and the empty rows would hide the real ones.
	rows := map[int64]*ApprovalRow{}
	rowFor := func(userID int64, name string) *ApprovalRow {
		row := rows[userID]
		if row == nil {
			row = &ApprovalRow{
				UserID: userID, UserName: name,
				Cells: make([]ApprovalCell, weeks),
			}
			for i := range row.Cells {
				row.Cells[i].WeekStart = report.Weeks[i].WeekStart
			}
			rows[userID] = row
		}
		// A name can arrive from either source; the first non-empty one wins.
		if row.UserName == "" {
			row.UserName = name
		}
		return row
	}

	// Recorded time first. The week an entry belongs to is decided in the
	// entry's own zone, exactly as everywhere else.
	for _, entry := range entries {
		if !entry.Counts() {
			continue
		}
		zone := loc
		if entry.TimeZone != "" {
			if parsed, zoneErr := time.LoadLocation(entry.TimeZone); zoneErr == nil {
				zone = parsed
			}
		}
		key := domain.WeekStartFor(entry.StartedAt, settings.WeekStart, zone)
		column, ok := columnAt[key]
		if !ok {
			// An entry at the edge of the range whose local week falls outside
			// it. Not an error, and not this report's business.
			continue
		}
		row := rowFor(entry.UserID, entry.UserName)
		row.Cells[column].Seconds += entry.DurationSeconds
		row.TotalSeconds += entry.DurationSeconds
	}

	// Then the submission state on top.
	for _, period := range periods {
		column, ok := columnAt[period.WeekStart]
		if !ok {
			continue
		}
		row := rowFor(period.UserID, period.UserName)
		cell := &row.Cells[column]
		cell.Status = string(period.Status)
		cell.SubmittedSeconds = period.SubmittedSeconds
		cell.Note = period.Note
		cell.Changed = period.Status == domain.PeriodSubmitted &&
			cell.Seconds != period.SubmittedSeconds
	}

	// Derive the statuses that are an absence rather than a row, and total the
	// columns.
	for _, row := range rows {
		for i := range row.Cells {
			cell := &row.Cells[i]
			week := &report.Weeks[i]

			switch domain.PeriodStatus(cell.Status) {
			case domain.PeriodSubmitted:
				week.Submitted++
			case domain.PeriodApproved:
				week.Approved++
			case domain.PeriodRejected:
				week.Rejected++
			}

			if cell.Status == "" || domain.PeriodStatus(cell.Status) == domain.PeriodOpen ||
				domain.PeriodStatus(cell.Status) == domain.PeriodRejected {
				// Open and rejected both mean "still with its owner". Whether
				// that is outstanding depends on whether there is anything in
				// it: a week nobody worked needs no submission.
				if cell.Recorded() {
					if cell.Status == "" || domain.PeriodStatus(cell.Status) == domain.PeriodOpen {
						cell.Status = string(StatusNotSubmitted)
					}
					week.NotSubmitted++
					row.Outstanding++
					report.Outstanding++
				} else if cell.Status == "" {
					cell.Status = string(StatusNothing)
				}
			} else if !cell.Recorded() {
				// Submitted or approved with nothing in it. Should not happen;
				// a restore or a deletion after an approval can do it, and it
				// is worth seeing rather than rendering as an ordinary tick.
				cell.Status = string(StatusEmptySubmission)
			}

			week.TotalSeconds += cell.Seconds
			report.TotalSeconds += cell.Seconds
		}
	}

	// A stable order: by name, because that is how somebody looks a person up.
	for _, row := range rows {
		report.Rows = append(report.Rows, *row)
	}
	sort.Slice(report.Rows, func(i, j int) bool {
		if report.Rows[i].UserName != report.Rows[j].UserName {
			return report.Rows[i].UserName < report.Rows[j].UserName
		}
		return report.Rows[i].UserID < report.Rows[j].UserID
	})
	return report, nil
}
