package service

import (
	"context"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The summary report: where the time went, over a range, grouped.
//
// The month view already answers this for one month and one person, broken down
// by assignment. What it cannot answer is the question somebody asks at the end
// of a quarter - "how much did we do for Acme, and what is it worth" - because
// that crosses months, crosses people, and groups by something other than the
// assignment.
//
// It is built from the same entry search every other screen uses rather than
// from SQL of its own. That is deliberate: the scope rules, the client
// narrowing and the money projection are enforced once, in that path, and a
// report with its own query would be a second place for them to be got wrong -
// which is exactly the shape of the defects fixed in the authorisation work
// that preceded this.

// GroupBy names the dimension a summary is grouped by.
type GroupBy string

const (
	// GroupByCustomer totals the work done for each customer. The default,
	// because it is the dimension an invoice and a conversation about one are
	// both organised around.
	GroupByCustomer GroupBy = "customer"
	// GroupByProject splits a customer's work by engagement.
	GroupByProject GroupBy = "project"
	// GroupByAssignment is the finest grouping: what the time was actually
	// recorded against.
	GroupByAssignment GroupBy = "assignment"
	// GroupByPerson totals by who did the work, which is the one grouping that
	// crosses customers rather than dividing them.
	GroupByPerson GroupBy = "person"
)

// Groupings are the dimensions offered, in the order a picker should show them.
var Groupings = []GroupBy{
	GroupByCustomer, GroupByProject, GroupByAssignment, GroupByPerson,
}

// OrDefault reduces an unrecognised grouping to the customer, which is the one
// most reports are about.
//
// Degrading rather than refusing, like every other presentation choice: a
// mistyped query string is not worth an error page, and letting it through
// would mean a report headed by a dimension nothing was grouped by.
func (g GroupBy) OrDefault() GroupBy {
	for _, known := range Groupings {
		if g == known {
			return g
		}
	}
	return GroupByCustomer
}

// SummaryRow is one group's figures.
type SummaryRow struct {
	// Key identifies the group for the template; Label is what people read.
	Key   string
	Label string
	// Detail is the line under the label: the customer a project belongs to,
	// the project an assignment belongs to. Empty where there is nothing to
	// say.
	Detail string
	// ColourKey carries the group's palette entry where it has one, so a
	// report reads in the same colours as the rest of the application.
	ColourKey string

	Seconds         int64
	BillableSeconds int64
	Entries         int
	// AmountMinor and Currency are the summed billing snapshot. Zero and empty
	// for somebody who may not see money, which is a projection applied before
	// the figures leave the service rather than a column the template hides.
	AmountMinor int64
	Currency    string
	// Percent is this group's share of the report's total seconds.
	Percent int
}

// SummaryReport is the whole answer.
type SummaryReport struct {
	From, To time.Time
	Group    GroupBy
	Rows     []SummaryRow
	Totals   domain.Totals
	// Money says whether the amounts are populated, so the screen knows
	// whether to draw the column at all.
	Money       bool
	AmountMinor int64
	Currency    string
	// Mixed is set when the range holds more than one currency, in which case
	// the total is not a number anybody should add up.
	Mixed bool
}

// Summarise groups the entries in a range.
func (s *Service) Summarise(ctx context.Context, from, to time.Time, group GroupBy) (SummaryReport, error) {
	group = group.OrDefault()
	report := SummaryReport{From: from, To: to, Group: group}

	// The same search every listing uses, so the scope and the client
	// narrowing are the ones already enforced there.
	entries, err := s.Entries(ctx, EntryFilter{From: from, To: to.AddDate(0, 0, 1)})
	if err != nil {
		return SummaryReport{}, err
	}

	report.Money = s.maySeeMoney(ctx)
	report.Totals = s.totals(entries)

	rows := map[string]*SummaryRow{}
	for _, entry := range entries {
		// Flagged and unconfirmed time is excluded for the same reason it is
		// excluded from every other total: it is not yet a statement about what
		// was worked (ADR-0033, ADR-0005).
		if !entry.Counts() {
			continue
		}

		key, label, detail, colour := groupOf(entry, group)
		row, seen := rows[key]
		if !seen {
			row = &SummaryRow{Key: key, Label: label, Detail: detail, ColourKey: colour}
			rows[key] = row
		}
		row.Entries++
		row.Seconds += entry.ElapsedSeconds(s.now())
		if entry.Billable {
			row.BillableSeconds += entry.BillableSeconds
		}

		if !report.Money {
			continue
		}
		row.AmountMinor += entry.AmountMinor
		if entry.Currency == "" {
			continue
		}
		if row.Currency == "" {
			row.Currency = entry.Currency
		}
		if report.Currency == "" {
			report.Currency = entry.Currency
		} else if report.Currency != entry.Currency {
			// Two currencies in one range. The per-row figures are still
			// correct; the grand total is not a number, and the screen says so
			// rather than printing a sum of kronor and euros.
			report.Mixed = true
		}
	}

	for _, row := range rows {
		if report.Totals.SummedSeconds > 0 {
			// Half away from zero in integers, so the shares read as whole
			// numbers and add to 100 as closely as rounding allows.
			row.Percent = int((row.Seconds*200 + report.Totals.SummedSeconds) /
				(report.Totals.SummedSeconds * 2))
		}
		report.AmountMinor += row.AmountMinor
		report.Rows = append(report.Rows, *row)
	}

	// Largest first, and by label when two are equal so the order is stable
	// between requests: a report that reshuffles on reload is one nobody
	// trusts with a figure.
	sort.Slice(report.Rows, func(a, b int) bool {
		if report.Rows[a].Seconds != report.Rows[b].Seconds {
			return report.Rows[a].Seconds > report.Rows[b].Seconds
		}
		return report.Rows[a].Label < report.Rows[b].Label
	})

	return report, nil
}

// groupOf returns the key, label, detail line and colour for an entry under a
// grouping.
func groupOf(entry domain.TimeEntry, group GroupBy) (key, label, detail, colour string) {
	switch group {
	case GroupByProject:
		return "project:" + entry.ProjectName + "/" + entry.CustomerName,
			nameOr(entry.ProjectName), entry.CustomerName, entry.ColourKey
	case GroupByAssignment:
		return "assignment:" + entry.AssignmentName + "/" + entry.ProjectName + "/" + entry.CustomerName,
			nameOr(entry.AssignmentName), entry.CustomerName + " · " + entry.ProjectName,
			entry.ColourKey
	case GroupByPerson:
		// By name rather than by id, because the name is what the report says
		// and two accounts with one name is a different problem.
		return "person:" + entry.UserName, nameOr(entry.UserName), "", ""
	default:
		return "customer:" + entry.CustomerName, nameOr(entry.CustomerName), "", entry.ColourKey
	}
}

// nameOr gives a group with no name something to be called.
//
// It happens: an entry on an assignment whose project has no customer name in
// a restored backup, or time recorded against a record since renamed to empty.
// A blank row in a report is worse than a named one, because it reads as a
// rendering fault rather than as data.
func nameOr(name string) string {
	if name == "" {
		return "—"
	}
	return name
}
