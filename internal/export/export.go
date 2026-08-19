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
	"iter"
	"sort"
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

// Meta is everything about a report except its rows.
//
// Split out from Report so a streaming writer can emit the header before it has
// seen a single entry, which is the whole difference between a download that
// starts immediately and one that begins after the database has been read.
type Meta struct {
	Title       string
	GeneratedAt time.Time
	From, To    time.Time
	TimeZone    string
	User        string
}

// Stream is a report that is written as it is read.
//
// Lines arrive in ascending order of start. That is required rather than
// incidental: the elapsed total is folded in one pass by domain.UnionAccumulator,
// which is only correct for intervals in ascending order.
type Stream struct {
	Meta  Meta
	Lines iter.Seq2[Line, error]
	// Now is the instant a running entry is measured against, as for NewReport.
	Now time.Time
}

// LinesOf turns a sequence of entries into report lines.
//
// The one place a TimeEntry becomes a Line, so a streamed export and a collected
// one cannot describe the same entry differently.
func LinesOf(entries iter.Seq2[domain.TimeEntry, error], now time.Time) iter.Seq2[Line, error] {
	return func(yield func(Line, error) bool) {
		for entry, err := range entries {
			if err != nil {
				yield(Line{}, err)
				return
			}
			if !yield(lineOf(entry, now), nil) {
				return
			}
		}
	}
}

// Primed pulls the first line before anything is written.
//
// A streamed response commits to its status code with its first byte, so an
// error that arrives after that can only be logged - the download is already a
// truncated file with a 200 on it. Most export failures are available before
// any byte is due, though: a malformed regular expression, a filter SQLite
// refuses, a closed database. Pulling one line first turns those back into an
// ordinary error the handler can report as a page, and costs one row of
// latency.
//
// It does not make the rest of the stream safe, and is not meant to. A failure
// halfway through ten thousand rows is still a truncated download; that is the
// price of not holding the report in memory.
//
// The returned sequence yields the pulled line first and then the rest, and -
// like the sequence it wraps - can only be ranged over once.
func Primed(lines iter.Seq2[Line, error]) (iter.Seq2[Line, error], error) {
	next, stop := iter.Pull2(lines)

	first, err, ok := next()
	if err != nil {
		stop()
		return nil, err
	}
	if !ok {
		// An empty range is not an error. The document is still written, with
		// no entries and zero totals, because a report of nothing is a valid
		// answer to a question about a quiet week.
		stop()
		return func(func(Line, error) bool) {}, nil
	}

	return func(yield func(Line, error) bool) {
		defer stop()
		if !yield(first, nil) {
			return
		}
		for {
			line, err, ok := next()
			if !ok {
				return
			}
			if !yield(line, err) {
				return
			}
		}
	}, nil
}

// Streams reports whether this format can be written without collecting the
// whole report first.
//
// CSV and JSON can: they are sequences of rows, and their totals - where they
// have any - can be folded as the rows go past. The document formats cannot,
// and not for want of trying: PDF, DOCX and Markdown all group by customer and
// project and print a subtotal per group, so the last row of the input can
// belong to the first group of the output. Grouping is buffering.
func (f Format) Streams() bool {
	return f == FormatCSV || f == FormatJSON
}

// WriteStream renders a streamed report. Only valid for a format that Streams.
func (f Format) WriteStream(w io.Writer, stream Stream) error {
	switch f {
	case FormatCSV:
		return WriteCSVStream(w, stream)
	case FormatJSON:
		return WriteJSONStream(w, stream)
	default:
		return fmt.Errorf("%w: %q cannot be streamed", ErrUnknownFormat, f)
	}
}

// streamOf turns a collected report into a stream.
//
// The collected writers are implemented on top of the streaming ones rather than
// beside them, so there is exactly one piece of code that knows how a CSV row or
// a JSON entry is shaped. Two implementations of one format is how the same
// request came to produce differently-formatted JSON depending on which filter
// was set.
//
// Lines are sorted ascending, because a stream requires it: the elapsed total is
// folded in one pass and that is only correct in ascending order.
func streamOf(report Report) Stream {
	lines := make([]Line, len(report.Lines))
	copy(lines, report.Lines)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Start.Before(lines[j].Start) })

	return Stream{
		Meta: Meta{
			Title:       report.Title,
			GeneratedAt: report.GeneratedAt,
			From:        report.From,
			To:          report.To,
			TimeZone:    report.TimeZone,
			User:        report.User,
		},
		Lines: func(yield func(Line, error) bool) {
			for _, line := range lines {
				if !yield(line, nil) {
					return
				}
			}
		},
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
	Kind   string
	Status string
	// Flagged marks an entry needing human review - a timer left running past
	// the maximum, say. It is carried because it is half of whether a line
	// counts, and a document whose subtotals include what its total excludes
	// does not add up.
	Flagged        bool
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
		line := lineOf(e, now)
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

// lineOf converts one entry into a report line.
func lineOf(e domain.TimeEntry, now time.Time) Line {
	return Line{
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
		Flagged:        e.Flagged,
		Seconds:        e.ElapsedSeconds(now),
		BillableSecond: e.BillableSeconds,
		RoundingRule:   e.RoundingRuleApplied,
		RateMinor:      e.RateMinor,
		AmountMinor:    e.AmountMinor,
		Currency:       e.Currency,
	}
}

// Counts reports whether a line contributes to totals.
//
// The same rule as domain.TimeEntry.Counts, restated on the line so a streaming
// writer can decide without the entry it came from - and so that every total in
// every format applies one rule. A pending proxy proposal is listed and billed
// to nobody; so is an entry flagged for review.
//
// The flagged half was missing from the document formats, whose per-group
// subtotals tested the status alone. A flagged entry therefore appeared in a
// subtotal and not in the grand total below it, so the columns of a client's
// PDF did not add up.
func (l Line) Counts() bool {
	return l.Status == string(domain.StatusConfirmed) && !l.Flagged
}
