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

// The wire shape of an exported report is a published contract that an
// invoicing system may have been written against, and is deliberately separate
// from Report: the internal representation is free to change, this is not.
// Durations appear both as seconds (exact) and as decimal hours (convenient);
// money appears as integer minor units with its currency, never as a float, so
// the consumer inherits our exactness rather than our rounding.
//
// The document is an object of
//
//	schema_version, title, generated_at, from, to, time_zone, user?
//	entries: [jsonEntry, ...]
//	totals:  jsonTotals
//
// in that order. There is no struct for the envelope because there is never a
// whole document in memory to put in one: WriteJSONStream writes the fields
// above by hand, precisely so a three-year export does not have to be
// assembled before it can be sent. The entries and the totals do have types,
// below, and those are what the field names and omitempty rules come from.
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
//
// Implemented on the streaming writer, so the same request cannot produce
// differently-formatted JSON depending on which path answered it.
func WriteJSON(w io.Writer, report Report) error {
	return WriteJSONStream(w, streamOf(report))
}

// jsonEntryOf converts one line to its published shape, so the streamed and the
// collected writers cannot describe the same entry differently.
func jsonEntryOf(line Line) jsonEntry {
	return jsonEntry{
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
	}
}

// WriteJSONStream renders a streamed report against the same schema.
//
// The document is assembled by hand rather than by encoding one value, because
// the whole point is not to have one value: the envelope is written first, the
// entries are encoded and released one at a time, and the totals - which depend
// on every entry - are written last, from figures folded as the entries went
// past. The published schema puts totals after entries, which is what makes
// that possible; had it put them first this format could not stream at all.
func WriteJSONStream(w io.Writer, stream Stream) error {
	meta := stream.Meta

	// The envelope, up to the entries array. Encoded through json.Marshal per
	// value rather than written as text, so a title containing a quote cannot
	// produce a broken document.
	if _, err := io.WriteString(w, "{\n"); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"schema_version", SchemaVersion},
		{"title", meta.Title},
		{"generated_at", meta.GeneratedAt},
		{"from", meta.From},
		{"to", meta.To},
		{"time_zone", meta.TimeZone},
	} {
		if err := writeJSONField(w, field.name, field.value, ",\n"); err != nil {
			return err
		}
	}
	// user carries omitempty in the collected shape, so it is omitted here too.
	if meta.User != "" {
		if err := writeJSONField(w, "user", meta.User, ",\n"); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, `  "entries": [`); err != nil {
		return err
	}

	totals := Summary{AmountsByCurrency: map[string]int64{}}
	var elapsed domain.UnionAccumulator
	first := true

	for line, err := range stream.Lines {
		if err != nil {
			return err
		}

		separator := ",\n    "
		if first {
			separator = "\n    "
			first = false
		}
		if _, err := io.WriteString(w, separator); err != nil {
			return err
		}
		encoded, err := json.Marshal(jsonEntryOf(line))
		if err != nil {
			return fmt.Errorf("encode entry %d: %w", line.ID, err)
		}
		if _, err := w.Write(encoded); err != nil {
			return err
		}

		// The same rule the collected report applies: a proposal nobody has
		// accepted, and an entry flagged for review, are listed and counted for
		// nothing.
		if !line.Counts() {
			continue
		}
		totals.EntryCount++
		totals.SummedSeconds += line.Seconds
		if line.Billable {
			totals.BillableSeconds += line.Seconds
		}
		if line.AmountMinor != 0 && line.Currency != "" {
			totals.AmountsByCurrency[line.Currency] += line.AmountMinor
		}
		start := line.Start.Unix()
		elapsed.Add(domain.Interval{Start: start, End: start + line.Seconds})
	}

	if elapsed.OutOfOrder {
		// The accumulator can only be right for intervals in ascending order,
		// and the query that feeds it orders them that way. If that ever stops
		// being true the total is wrong, and a wrong total on an invoice is not
		// something to discover later.
		return fmt.Errorf("entries arrived out of order; the elapsed total would be wrong")
	}
	totals.ElapsedSeconds = elapsed.Seconds()
	totals.OverlapSeconds = totals.SummedSeconds - totals.ElapsedSeconds

	closing := "\n  ],\n"
	if first {
		closing = "],\n"
	}
	if _, err := io.WriteString(w, closing); err != nil {
		return err
	}
	if err := writeJSONField(w, "totals", jsonTotalsOf(totals), "\n"); err != nil {
		return err
	}
	_, err := io.WriteString(w, "}\n")
	return err
}

// jsonTotalsOf converts a summary to its published shape.
func jsonTotalsOf(totals Summary) jsonTotals {
	amounts := totals.AmountsByCurrency
	if amounts == nil {
		// An empty object is friendlier to a consumer than a null.
		amounts = map[string]int64{}
	}
	return jsonTotals{
		EntryCount:        totals.EntryCount,
		SummedSeconds:     totals.SummedSeconds,
		SummedHours:       domain.FormatDecimalHours(totals.SummedSeconds),
		ElapsedSeconds:    totals.ElapsedSeconds,
		OverlapSeconds:    totals.OverlapSeconds,
		BillableSeconds:   totals.BillableSeconds,
		BillableHours:     domain.FormatDecimalHours(totals.BillableSeconds),
		AmountsByCurrency: amounts,
	}
}

// writeJSONField writes one indented "name": value pair.
func writeJSONField(w io.Writer, name string, value any, suffix string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	if _, err := fmt.Fprintf(w, "  %q: %s%s", name, encoded, suffix); err != nil {
		return err
	}
	return nil
}
