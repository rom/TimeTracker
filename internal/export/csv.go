package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// WriteCSVStream renders a streamed report as CSV.
//
// The natural streaming format: a header and then one row per entry, with
// nothing at the end that depends on what came before. Rows go to the writer as
// they arrive, so a decade of entries costs no more memory than one.
func WriteCSVStream(w io.Writer, stream Stream) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return fmt.Errorf("write BOM: %w", err)
	}

	writer := csv.NewWriter(w)
	defer writer.Flush()

	if err := writer.Write(csvHeader); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}

	loc := locationOf(stream.Meta.TimeZone)
	for line, err := range stream.Lines {
		if err != nil {
			return err
		}
		if err := writer.Write(csvRecord(line, loc)); err != nil {
			return fmt.Errorf("write CSV row: %w", err)
		}
		// Flushed per row rather than at the end: the point of streaming is that
		// the client starts receiving before the database has finished being
		// read, and an unflushed buffer defers exactly that.
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
	}
	return writer.Error()
}

// csvHeader is the column list, shared by both paths so they cannot disagree.
var csvHeader = []string{
	"id", "date", "start", "end", "customer", "project", "assignment",
	"note", "tags", "user", "entered_by", "billable", "kind", "status",
	"duration_hours", "duration_seconds",
	"billable_hours", "rounding_rule", "rate", "amount", "currency",
}

// csvRecord renders one line, likewise shared.
func csvRecord(line Line, loc *time.Location) []string {
	end := ""
	if line.End != nil {
		end = line.End.In(loc).Format(time.RFC3339)
	}
	return []string{
		strconv.FormatInt(line.ID, 10),
		line.Date.Format("2006-01-02"),
		line.Start.In(loc).Format(time.RFC3339),
		end,
		line.Customer,
		line.Project,
		line.Assignment,
		line.Note,
		// Space separated, which is how they are typed into the search box
		// and how a spreadsheet filter can match one of several.
		strings.Join(line.Tags, " "),
		line.User,
		line.EnteredBy,
		strconv.FormatBool(line.Billable),
		line.Kind,
		line.Status,
		// Decimal hours is what invoicing systems consume; raw seconds is
		// carried alongside so nothing is lost to rounding on the way out.
		domain.FormatDecimalHours(line.Seconds),
		strconv.FormatInt(line.Seconds, 10),
		domain.FormatDecimalHours(line.BillableSecond),
		line.RoundingRule,
		domain.NewMoney(line.RateMinor, line.Currency).Decimal(),
		domain.NewMoney(line.AmountMinor, line.Currency).Decimal(),
		line.Currency,
	}
}

// WriteCSV renders a report as CSV, one row per entry.
//
// The output is UTF-8 with a byte order mark. That BOM is not decoration: without
// it, Excel on Windows reads the file as the system code page and mangles every
// non-ASCII customer name, which is the single most common complaint about
// exported timesheets.
//
// Implemented on the streaming writer so there is one definition of a row.
func WriteCSV(w io.Writer, report Report) error {
	return WriteCSVStream(w, streamOf(report))
}

// locationOf resolves a zone name, defaulting to UTC.
func locationOf(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}
