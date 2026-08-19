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
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Format is an output encoding.
type Format string

const (
	FormatCSV  Format = "csv"
	FormatJSON Format = "json"
	FormatPDF  Format = "pdf"
	FormatDOCX Format = "docx"
	FormatMD   Format = "md"
)

// Formats lists every encoding, in the order they are offered.
//
// The order is by what somebody reaches for: the two a client receives, then
// the one that goes into a ticket or an email, then the two a machine consumes.
// It is a single list so that adding a format cannot leave the interface
// offering four of them and the router accepting five.
var Formats = []Format{FormatPDF, FormatDOCX, FormatMD, FormatCSV, FormatJSON}

// ContentType returns the MIME type a format is served as.
func (f Format) ContentType() string {
	switch f {
	case FormatCSV:
		return "text/csv; charset=utf-8"
	case FormatJSON:
		return "application/json; charset=utf-8"
	case FormatPDF:
		return "application/pdf"
	case FormatDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case FormatMD:
		return "text/markdown; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Write renders a report in this format.
//
// Dispatching here rather than in the HTTP handler is what keeps the two lists
// in step: a format in Formats with no case below fails its own test rather
// than reaching a user as an empty download.
func (f Format) Write(w io.Writer, report Report) error {
	switch f {
	case FormatCSV:
		return WriteCSV(w, report)
	case FormatJSON:
		return WriteJSON(w, report)
	case FormatPDF:
		return WritePDF(w, report)
	case FormatDOCX:
		return WriteDOCX(w, report)
	case FormatMD:
		return WriteMarkdown(w, report)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownFormat, f)
	}
}

// ErrUnknownFormat is returned for an encoding this package does not produce.
var ErrUnknownFormat = errors.New("unknown export format")

// Known reports whether this is a format the package can write.
func (f Format) Known() bool {
	for _, known := range Formats {
		if f == known {
			return true
		}
	}
	return false
}

// Label is the name shown on a download button.
func (f Format) Label() string {
	switch f {
	case FormatMD:
		return "Markdown"
	default:
		return strings.ToUpper(string(f))
	}
}

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
			Tags:           e.Tags,
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
