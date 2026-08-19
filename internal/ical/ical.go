// Package ical reads iCalendar (RFC 5545) files.
//
// Enough of the format to import somebody's calendar as time entries, and no
// more. Outlook, Google Calendar and Apple Calendar all export .ics, and all
// three produce files this reads; a full implementation would need recurrence
// expansion with time-zone-aware exceptions, alarms, attendees and free/busy,
// none of which a timesheet cares about.
//
// What it does handle is the part that actually breaks naive parsers:
//
//   - line folding. RFC 5545 wraps long lines at 75 octets and continues them
//     with a leading space or tab. A parser that reads line by line splits
//     summaries mid-word, and the damage is invisible until somebody notices a
//     truncated meeting name.
//   - escaping. Commas, semicolons and newlines inside a value arrive
//     backslash-escaped.
//   - the three DTSTART forms: UTC ("...Z"), floating local time, and a date
//     with no time at all for an all-day event.
//   - CRLF, LF, and the mixture a file that has been through a mail client has.
package ical

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
)

// Event is one VEVENT, reduced to what a timesheet needs.
type Event struct {
	// UID identifies the event in its calendar. Carried so an import can tell
	// that it has seen this event before.
	UID     string
	Summary string
	// Description and Location are offered because they often carry the only
	// hint of which customer a meeting belongs to.
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	// AllDay marks an event given as a date with no time. Such an event has no
	// duration worth importing, and the caller decides what to do about it.
	AllDay bool
	// Cancelled and Declined mark events that should not become time. An event
	// somebody declined is one they did not attend.
	Cancelled bool
	Declined  bool
	// Recurring marks an event with a recurrence rule. Only the first instance
	// is returned; see Parse.
	Recurring bool
}

// Duration is how long the event lasts.
func (e Event) Duration() time.Duration {
	if e.End.IsZero() || !e.End.After(e.Start) {
		return 0
	}
	return e.End.Sub(e.Start)
}

// Parse reads every VEVENT from an iCalendar stream.
//
// Recurrence rules are *not* expanded. An RRULE describes an infinite series,
// and expanding one correctly needs the exception dates, the time zone the rule
// is anchored in, and a decision about how far into the future to go. Getting it
// subtly wrong would silently invent meetings that never happened. The first
// instance is returned with Recurring set, and the caller can say so.
func Parse(r io.Reader) ([]Event, error) {
	lines, err := unfold(r)
	if err != nil {
		return nil, err
	}

	var events []Event
	var current *Event
	// The zone a floating DTSTART is interpreted in, taken from the calendar's
	// X-WR-TIMEZONE when it has one. Google sets it; Outlook does not.
	calendarZone := time.Local

	for _, line := range lines {
		name, params, value := splitLine(line)

		switch strings.ToUpper(name) {
		case "BEGIN":
			if strings.EqualFold(value, "VEVENT") {
				current = &Event{}
			}
		case "END":
			if strings.EqualFold(value, "VEVENT") && current != nil {
				events = append(events, *current)
				current = nil
			}
		case "X-WR-TIMEZONE":
			if loaded, loadErr := time.LoadLocation(unescape(value)); loadErr == nil {
				calendarZone = loaded
			}
		}
		if current == nil {
			continue
		}

		switch strings.ToUpper(name) {
		case "UID":
			current.UID = unescape(value)
		case "SUMMARY":
			current.Summary = unescape(value)
		case "DESCRIPTION":
			current.Description = unescape(value)
		case "LOCATION":
			current.Location = unescape(value)
		case "RRULE":
			current.Recurring = true
		case "STATUS":
			current.Cancelled = strings.EqualFold(value, "CANCELLED")
		case "DTSTART":
			start, allDay, parseErr := parseTimestamp(value, params, calendarZone)
			if parseErr != nil {
				return nil, fmt.Errorf("DTSTART: %w", parseErr)
			}
			current.Start, current.AllDay = start, allDay
		case "DTEND":
			end, _, parseErr := parseTimestamp(value, params, calendarZone)
			if parseErr != nil {
				return nil, fmt.Errorf("DTEND: %w", parseErr)
			}
			current.End = end
		case "DURATION":
			// An event may give a length instead of an end. Both forms are
			// legal and Outlook emits either depending on how the event was
			// created.
			if length, durErr := parseDuration(value); durErr == nil && !current.Start.IsZero() {
				current.End = current.Start.Add(length)
			}
		case "ATTENDEE":
			// PARTSTAT=DECLINED on the attendee line is how a calendar records
			// that this person is not going. Importing a declined meeting as
			// time worked would be inventing an hour.
			if strings.Contains(strings.ToUpper(params), "PARTSTAT=DECLINED") {
				current.Declined = true
			}
		}
	}

	if current != nil {
		return nil, fmt.Errorf("the file ends inside an event; it is truncated")
	}
	return events, nil
}

// unfold reads the stream and rejoins folded lines.
//
// RFC 5545 wraps at 75 octets and continues with a leading space or tab. A
// parser that reads line by line splits values mid-word, and the result is a
// meeting called "Quarterly review with Acm" followed by a line nobody parses.
func unfold(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	// A single folded value can be long - a description with an agenda in it -
	// so the default 64KB token limit is raised.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var lines []string
	for scanner.Scan() {
		// Trailing CR from a CRLF file read on a system that splits on LF.
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read calendar: %w", err)
	}
	return lines, nil
}

// splitLine separates a content line into its name, its parameters and its
// value: "DTSTART;TZID=Europe/Stockholm:20260318T090000".
func splitLine(line string) (name, params, value string) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return line, "", ""
	}
	left, value := line[:colon], line[colon+1:]

	if semicolon := strings.IndexByte(left, ';'); semicolon >= 0 {
		return left[:semicolon], left[semicolon+1:], value
	}
	return left, "", value
}

// parseTimestamp reads the three DTSTART/DTEND forms.
func parseTimestamp(value, params string, fallback *time.Location) (time.Time, bool, error) {
	value = strings.TrimSpace(value)

	// A date with no time: an all-day event.
	if len(value) == 8 && !strings.ContainsAny(value, "TZ") {
		parsed, err := time.ParseInLocation("20060102", value, locationFor(params, fallback))
		return parsed, true, err
	}

	// UTC, marked by a trailing Z.
	if strings.HasSuffix(value, "Z") {
		parsed, err := time.Parse("20060102T150405Z", value)
		return parsed, false, err
	}

	// Floating local time, possibly with a TZID parameter naming the zone.
	parsed, err := time.ParseInLocation("20060102T150405", value, locationFor(params, fallback))
	return parsed, false, err
}

// locationFor reads a TZID parameter, falling back to the calendar's zone.
//
// An unknown zone name falls back rather than failing: zone databases differ
// between systems, and refusing a whole calendar because one meeting names a
// zone this machine has not heard of would be a poor trade.
func locationFor(params string, fallback *time.Location) *time.Location {
	for _, part := range strings.Split(params, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "TZID") {
			continue
		}
		name := strings.Trim(strings.TrimSpace(value), `"`)
		if loaded, err := time.LoadLocation(name); err == nil {
			return loaded
		}
	}
	if fallback == nil {
		return time.UTC
	}
	return fallback
}

// parseDuration reads an RFC 5545 duration: "PT1H30M", "P1D", "-PT15M".
func parseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimLeft(value, "+-")
	if !strings.HasPrefix(value, "P") {
		return 0, fmt.Errorf("%q is not a duration", value)
	}
	value = value[1:]

	var total time.Duration
	var number int
	inTime := false
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
			number = number*10 + int(char-'0')
		case char == 'T':
			inTime = true
		case char == 'W':
			total += time.Duration(number) * 7 * 24 * time.Hour
			number = 0
		case char == 'D':
			total += time.Duration(number) * 24 * time.Hour
			number = 0
		case char == 'H':
			total += time.Duration(number) * time.Hour
			number = 0
		case char == 'M' && inTime:
			total += time.Duration(number) * time.Minute
			number = 0
		case char == 'S':
			total += time.Duration(number) * time.Second
			number = 0
		default:
			return 0, fmt.Errorf("%q is not a duration", value)
		}
	}
	if negative {
		total = -total
	}
	return total, nil
}

// unescape reverses RFC 5545 text escaping.
func unescape(value string) string {
	if !strings.ContainsRune(value, '\\') {
		return value
	}
	var out strings.Builder
	out.Grow(len(value))

	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			out.WriteByte(value[i])
			continue
		}
		i++
		switch value[i] {
		case 'n', 'N':
			out.WriteByte('\n')
		case '\\', ',', ';':
			out.WriteByte(value[i])
		default:
			// An escape this format does not define: keep both characters
			// rather than silently eating one.
			out.WriteByte('\\')
			out.WriteByte(value[i])
		}
	}
	return out.String()
}
