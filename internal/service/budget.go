package service

import (
	"context"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Budget consumption and burn (ADR-0035).
//
// Two figures and a guess. The figures are what has been recorded against a
// project's cap; the guess is when the cap runs out at the recent rate, and it
// is presented as a guess or not at all.

// BudgetRow is one project's line on the report.
type BudgetRow struct {
	domain.BudgetLine
	Burn domain.BurnProjection
}

// BudgetReport is the whole thing, with the window the rate was taken over so a
// reader can see what the estimate is based on.
type BudgetReport struct {
	Rows []BudgetRow
	// WindowFrom and WindowTo bound the recent window the burn rate averages
	// over, and WindowWeeks is its length.
	WindowFrom  time.Time
	WindowTo    time.Time
	WindowWeeks int
}

// Overruns counts the rows past a cap, for a heading that says so without
// making a reader scan the table.
func (r BudgetReport) Overruns() int {
	var n int
	for _, row := range r.Rows {
		if row.Overrun() {
			n++
		}
	}
	return n
}

// BudgetReportFor builds the burn report for every budgeted project in scope.
//
// Three queries regardless of how many projects there are: the budgeted
// projects, their total consumption, and the recent window. A report that
// queried per project would be fine on a demo and quadratic on a consultancy.
func (s *Service) BudgetReportFor(ctx context.Context, now time.Time) (BudgetReport, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return BudgetReport{}, err
	}
	// A budget is a commercial fact about an engagement rather than about
	// anybody's timesheet, so seeing one takes the permission that governs
	// commercial facts. Without it there is no report at all - not a redacted
	// one, since a budget report with the budgets removed is an empty table with
	// a heading.
	if err := s.authz.Can(ctx, auth.ActionViewMoney, auth.Resource{Type: "project"}); err != nil {
		return BudgetReport{}, notFoundFor(err)
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return BudgetReport{}, err
	}

	projects, err := s.db.BudgetedProjects(ctx, scope)
	if err != nil {
		return BudgetReport{}, err
	}

	loc := locationFor(actor)
	local := now.In(loc)
	// The window ends at the end of today rather than at this instant, so work
	// recorded this morning is inside the window that is meant to describe
	// recent work.
	windowTo := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).
		AddDate(0, 0, 1)
	windowFrom := windowTo.AddDate(0, 0, -7*domain.BurnWindowWeeks)

	report := BudgetReport{
		WindowFrom: windowFrom, WindowTo: windowTo,
		WindowWeeks: domain.BurnWindowWeeks,
	}
	if len(projects) == 0 {
		return report, nil
	}

	total, err := s.db.ConsumptionByProject(ctx, scope, time.Time{}, time.Time{})
	if err != nil {
		return BudgetReport{}, err
	}
	recent, err := s.db.ConsumptionByProject(ctx, scope, windowFrom, windowTo)
	if err != nil {
		return BudgetReport{}, err
	}
	activeWeeks, err := s.db.ActiveWeeksByProject(ctx, scope, windowFrom, windowTo)
	if err != nil {
		return BudgetReport{}, err
	}

	for _, project := range projects {
		used := total[project.ID]
		window := recent[project.ID]

		line := domain.BudgetLine{
			ProjectID:    project.ID,
			ProjectName:  project.Name,
			CustomerID:   project.CustomerID,
			CustomerName: project.CustomerName,
			ColourKey:    project.ColourKey,
			Currency:     project.Currency,
			Archived:     project.Archived(),
			Budget: domain.Budget{
				Seconds: project.BudgetSeconds,
				Minor:   project.BudgetMinor,
			},
			UsedSeconds:   used.Seconds,
			UsedMinor:     used.Minor,
			RecentSeconds: window.Seconds,
			RecentMinor:   window.Minor,
			ActiveWeeks:   activeWeeks[project.ID],
			FirstEntry:    used.FirstEntry,
			LastEntry:     used.LastEntry,
		}
		report.Rows = append(report.Rows, BudgetRow{
			BudgetLine: line,
			Burn:       domain.ProjectBurn(line, now),
		})
	}

	// Worst first: the point of the screen is the projects in trouble, and a
	// reader who has to sort by hand will read the first five rows and stop.
	sortBudgetRows(report.Rows)
	return report, nil
}

// sortBudgetRows orders by how close each project is to its cap, most consumed
// first. Stable, and with a total tie-break, so the order does not shuffle
// between two renders of the same data.
func sortBudgetRows(rows []BudgetRow) {
	sort.SliceStable(rows, func(i, j int) bool { return budgetRowBefore(rows[i], rows[j]) })
}

func budgetRowBefore(a, b BudgetRow) bool {
	if af, bf := a.UsedFraction(), b.UsedFraction(); af != bf {
		return af > bf
	}
	// An archived project sinks below a live one at the same consumption: both
	// are worth showing, only one is worth acting on.
	if a.Archived != b.Archived {
		return b.Archived
	}
	if a.CustomerName != b.CustomerName {
		return a.CustomerName < b.CustomerName
	}
	return a.ProjectName < b.ProjectName
}
