// Package export renders a report into the formats a client or an invoicing
// system expects.
//
// Every format is produced from one Report value, so a CSV and a JSON of the same
// range cannot disagree about the numbers - which is the property that matters
// when one of them is the basis of an invoice and the other is what the client
// checks it against.
//
// Nothing here spawns a process or reaches the network: PDF and DOCX are
// generated in pure Go for the same reason the SQLite driver is
// (docs/adr/0007-pure-go-document-generation.md).
package export

import (
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Format is an output encoding.
type Format string

const (
	FormatCSV  Format = "csv"
	FormatJSON Format = "json"
	FormatPDF  Format = "pdf"  // planned: layer 5
	FormatDOCX Format = "docx" // planned: layer 5
)

// Report is the single in-memory representation every writer renders.
type Report struct {
	// Title is what appears at the head of a rendered document.
	Title string
	// GeneratedAt is when this report was produced, in UTC.
	GeneratedAt time.Time
	// From and To bound the period. To is exclusive.
	From time.Time
	To   time.Time
	// TimeZone is the IANA zone the period and the per-entry local times are
	// expressed in, recorded so a reader can reproduce the same figures.
	TimeZone string
	// User is who the report is for, when it covers one person.
	User string

	Lines  []Line
	Totals Summary
}

// Line is one time entry as it appears in a report.
//
// Both the recorded and the billable duration are carried, along with the
// rounding rule that produced the latter, so a client can verify the arithmetic
// instead of having to trust it.
type Line struct {
	ID         int64
	Date       time.Time
	Start      time.Time
	End        *time.Time
	Customer   string
	Project    string
	Assignment string
	Code       string
	Note       string
	User       string
	EnteredBy  string
	Billable   bool
	// Kind is work, overtime or travel. It is exported because a line billed at
	// one and a half times the base rate has to say why: an invoice figure that
	// cannot be explained from the document it appears in is a dispute waiting
	// to happen.
	Kind           string
	Status         string
	Seconds        int64
	BillableSecond int64
	RoundingRule   string
	RateMinor      int64
	AmountMinor    int64
	Currency       string
	Tags           []string
}

// Summary is the set of totals at the foot of a report.
type Summary struct {
	// SummedSeconds is the sum of the line durations - what gets billed.
	SummedSeconds int64
	// ElapsedSeconds is the union of the intervals: how much wall-clock time the
	// work covered. It differs from SummedSeconds when timers overlapped, and
	// both are reported so that difference is visible rather than alarming.
	ElapsedSeconds  int64
	OverlapSeconds  int64
	BillableSeconds int64
	// Amounts are totalled per currency. There is no conversion anywhere in this
	// application, so a mixed-currency report shows several totals rather than
	// one meaningless number.
	AmountsByCurrency map[string]int64
	EntryCount        int
}

// NewReport builds a Report from a set of entries.
func NewReport(title string, from, to time.Time, timeZone, user string, entries []domain.TimeEntry, now time.Time) Report {
	report := Report{
		Title:       title,
		GeneratedAt: now.UTC(),
		From:        from,
		To:          to,
		TimeZone:    timeZone,
		User:        user,
		Totals:      Summary{AmountsByCurrency: map[string]int64{}},
	}

	intervals := make([]domain.Interval, 0, len(entries))
	for _, e := range entries {
		line := Line{
			ID:             e.ID,
			Date:           e.LocalDay(),
			Start:          e.StartedAt,
			End:            e.EndedAt,
			Customer:       e.CustomerName,
			Project:        e.ProjectName,
			Assignment:     e.AssignmentName,
			Note:           e.Note,
			User:           e.UserName,
			EnteredBy:      e.EnteredByName,
			Billable:       e.Billable,
			Kind:           string(e.KindOrDefault()),
			Status:         string(e.Status),
			Seconds:        e.ElapsedSeconds(now),
			BillableSecond: e.BillableSeconds,
			RoundingRule:   e.RoundingRuleApplied,
			RateMinor:      e.RateMinor,
			AmountMinor:    e.AmountMinor,
			Currency:       e.Currency,
		}
		report.Lines = append(report.Lines, line)

		// Only entries that count contribute to the totals: a pending proxy
		// proposal is visible in the listing but must not be billed.
		if !e.Counts() {
			continue
		}
		report.Totals.EntryCount++
		report.Totals.SummedSeconds += line.Seconds
		if e.Billable {
			report.Totals.BillableSeconds += line.Seconds
		}
		if e.AmountMinor != 0 && e.Currency != "" {
			report.Totals.AmountsByCurrency[e.Currency] += e.AmountMinor
		}
		start := e.StartedAt.Unix()
		intervals = append(intervals, domain.Interval{Start: start, End: start + line.Seconds})
	}

	report.Totals.ElapsedSeconds = domain.UnionSeconds(intervals)
	report.Totals.OverlapSeconds = report.Totals.SummedSeconds - report.Totals.ElapsedSeconds
	return report
}
