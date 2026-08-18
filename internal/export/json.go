package export

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// SchemaVersion identifies the shape of the JSON export.
//
// It is emitted with every document so a consumer can tell whether it
// understands what it has been given. It is bumped when a field changes meaning
// or disappears - never when one is merely added.
const SchemaVersion = "1.0"

// jsonReport is the wire shape of an exported report.
//
// It is a separate type from Report deliberately: the internal representation is
// free to change, while this is a published contract that an invoicing system may
// have been written against. Durations appear both as seconds (exact) and as
// decimal hours (convenient); money appears as integer minor units with its
// currency, never as a float, so the consumer inherits our exactness rather than
// our rounding.
type jsonReport struct {
	SchemaVersion string      `json:"schema_version"`
	Title         string      `json:"title"`
	GeneratedAt   time.Time   `json:"generated_at"`
	From          time.Time   `json:"from"`
	To            time.Time   `json:"to"`
	TimeZone      string      `json:"time_zone"`
	User          string      `json:"user,omitempty"`
	Entries       []jsonEntry `json:"entries"`
	Totals        jsonTotals  `json:"totals"`
}

type jsonEntry struct {
	ID         int64      `json:"id"`
	Date       string     `json:"date"`
	Start      time.Time  `json:"start"`
	End        *time.Time `json:"end,omitempty"`
	Customer   string     `json:"customer"`
	Project    string     `json:"project"`
	Assignment string     `json:"assignment"`
	Note       string     `json:"note,omitempty"`
	User       string     `json:"user"`
	EnteredBy  string     `json:"entered_by,omitempty"`
	Billable   bool       `json:"billable"`
	// Kind explains a rate that differs from the customer's base one.
	Kind            string   `json:"kind"`
	Status          string   `json:"status"`
	DurationSeconds int64    `json:"duration_seconds"`
	DurationHours   string   `json:"duration_hours"`
	BillableSeconds int64    `json:"billable_seconds"`
	RoundingRule    string   `json:"rounding_rule,omitempty"`
	RateMinor       int64    `json:"rate_minor,omitempty"`
	AmountMinor     int64    `json:"amount_minor,omitempty"`
	Currency        string   `json:"currency,omitempty"`
	Tags            []string `json:"tags,omitempty"`
}

type jsonTotals struct {
	EntryCount int `json:"entry_count"`
	// Summed is what is billed; Elapsed is the wall-clock coverage. They differ
	// when timers overlapped, and both are published so a consumer does not have
	// to guess which one we meant.
	SummedSeconds     int64            `json:"summed_seconds"`
	SummedHours       string           `json:"summed_hours"`
	ElapsedSeconds    int64            `json:"elapsed_seconds"`
	OverlapSeconds    int64            `json:"overlap_seconds"`
	BillableSeconds   int64            `json:"billable_seconds"`
	BillableHours     string           `json:"billable_hours"`
	AmountsByCurrency map[string]int64 `json:"amounts_by_currency_minor"`
}

// WriteJSON renders a report as JSON against the documented schema.
func WriteJSON(w io.Writer, report Report) error {
	doc := jsonReport{
		SchemaVersion: SchemaVersion,
		Title:         report.Title,
		GeneratedAt:   report.GeneratedAt,
		From:          report.From,
		To:            report.To,
		TimeZone:      report.TimeZone,
		User:          report.User,
		Entries:       make([]jsonEntry, 0, len(report.Lines)),
		Totals: jsonTotals{
			EntryCount:        report.Totals.EntryCount,
			SummedSeconds:     report.Totals.SummedSeconds,
			SummedHours:       domain.FormatDecimalHours(report.Totals.SummedSeconds),
			ElapsedSeconds:    report.Totals.ElapsedSeconds,
			OverlapSeconds:    report.Totals.OverlapSeconds,
			BillableSeconds:   report.Totals.BillableSeconds,
			BillableHours:     domain.FormatDecimalHours(report.Totals.BillableSeconds),
			AmountsByCurrency: report.Totals.AmountsByCurrency,
		},
	}
	if doc.Totals.AmountsByCurrency == nil {
		// An empty object is friendlier to a consumer than a null.
		doc.Totals.AmountsByCurrency = map[string]int64{}
	}

	for _, line := range report.Lines {
		doc.Entries = append(doc.Entries, jsonEntry{
			ID:              line.ID,
			Date:            line.Date.Format("2006-01-02"),
			Start:           line.Start,
			End:             line.End,
			Customer:        line.Customer,
			Project:         line.Project,
			Assignment:      line.Assignment,
			Note:            line.Note,
			User:            line.User,
			EnteredBy:       line.EnteredBy,
			Billable:        line.Billable,
			Kind:            line.Kind,
			Status:          line.Status,
			DurationSeconds: line.Seconds,
			DurationHours:   domain.FormatDecimalHours(line.Seconds),
			BillableSeconds: line.BillableSecond,
			RoundingRule:    line.RoundingRule,
			RateMinor:       line.RateMinor,
			AmountMinor:     line.AmountMinor,
			Currency:        line.Currency,
			Tags:            line.Tags,
		})
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	return nil
}
