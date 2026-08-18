package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// WriteCSV renders a report as CSV, one row per entry.
//
// The output is UTF-8 with a byte order mark. That BOM is not decoration: without
// it, Excel on Windows reads the file as the system code page and mangles every
// non-ASCII customer name, which is the single most common complaint about
// exported timesheets.
func WriteCSV(w io.Writer, report Report) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return fmt.Errorf("write BOM: %w", err)
	}

	writer := csv.NewWriter(w)
	defer writer.Flush()

	header := []string{
		"id", "date", "start", "end", "customer", "project", "assignment",
		"note", "user", "entered_by", "billable", "kind", "status",
		"duration_hours", "duration_seconds",
		"billable_hours", "rounding_rule", "rate", "amount", "currency",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}

	loc := reportLocation(report)
	for _, line := range report.Lines {
		end := ""
		if line.End != nil {
			end = line.End.In(loc).Format(time.RFC3339)
		}
		record := []string{
			strconv.FormatInt(line.ID, 10),
			line.Date.Format("2006-01-02"),
			line.Start.In(loc).Format(time.RFC3339),
			end,
			line.Customer,
			line.Project,
			line.Assignment,
			line.Note,
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
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write CSV row: %w", err)
		}
	}

	writer.Flush()
	return writer.Error()
}

// reportLocation resolves the report's zone, defaulting to UTC.
func reportLocation(report Report) *time.Location {
	if report.TimeZone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(report.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}
