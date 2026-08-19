package ical

import (
	"strings"
	"testing"
	"time"
)

// The parser is tested against what the three calendars people actually use
// emit, because the failures that matter are format details rather than logic:
// a folded line silently truncates a meeting name, and a declined event
// silently becomes an hour somebody did not work.

func TestParseGoogleExport(t *testing.T) {
	// Google folds long lines, sets X-WR-TIMEZONE, and writes UTC timestamps.
	const file = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"X-WR-TIMEZONE:Europe/Stockholm\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:abc123@google.com\r\n" +
		"DTSTART:20260318T080000Z\r\n" +
		"DTEND:20260318T093000Z\r\n" +
		"SUMMARY:Quarterly review with Acme about the migration and the next\r\n" +
		"  phase of work\r\n" +
		"DESCRIPTION:Agenda:\\n1. Progress\\n2. Budget\r\n" +
		"LOCATION:Meeting room 3\\, second floor\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	events, err := Parse(strings.NewReader(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	event := events[0]

	// The folded line is rejoined. Reading line by line would give
	// "…and the next" and lose the rest, which nobody notices until later.
	want := "Quarterly review with Acme about the migration and the next phase of work"
	if event.Summary != want {
		t.Errorf("summary = %q,\n         want %q", event.Summary, want)
	}
	if event.Duration() != 90*time.Minute {
		t.Errorf("duration = %s, want 1h30m", event.Duration())
	}
	if !event.Start.Equal(time.Date(2026, 3, 18, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %s", event.Start)
	}
	// Escaped separators and newlines come back as themselves.
	if !strings.Contains(event.Description, "\n1. Progress") {
		t.Errorf("description = %q", event.Description)
	}
	if event.Location != "Meeting room 3, second floor" {
		t.Errorf("location = %q", event.Location)
	}
	if event.UID != "abc123@google.com" {
		t.Errorf("uid = %q", event.UID)
	}
}

func TestParseOutlookExport(t *testing.T) {
	// Outlook writes local times with a TZID, sometimes a DURATION instead of a
	// DTEND, and folds with a tab.
	const file = "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:040000008200E00074C5B7101A82E008\r\n" +
		"DTSTART;TZID=\"Europe/Stockholm\":20260318T090000\r\n" +
		"DURATION:PT45M\r\n" +
		"SUMMARY:Stand-up\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:second\r\n" +
		"DTSTART;TZID=Europe/Stockholm:20260318T130000\r\n" +
		"DTEND;TZID=Europe/Stockholm:20260318T140000\r\n" +
		"SUMMARY:Workshop \r\n" +
		"\tcontinued\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	events, err := Parse(strings.NewReader(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	if events[0].Duration() != 45*time.Minute {
		t.Errorf("a DURATION instead of a DTEND gave %s", events[0].Duration())
	}
	// The TZID decides the instant. Stockholm is UTC+1 in March before the
	// clocks change, so 09:00 local is 08:00 UTC.
	if got := events[0].Start.UTC().Format("15:04"); got != "08:00" {
		t.Errorf("start in UTC = %s, want 08:00 (the TZID was ignored)", got)
	}
	// Folding is byte-exact: the CRLF and the single whitespace that follows it
	// are the fold marker and nothing else, so unfolding introduces no space.
	// The space here is part of the value, written before the fold - which is
	// how a real exporter emits it.
	if events[1].Summary != "Workshop continued" {
		t.Errorf("tab-folded summary = %q", events[1].Summary)
	}
}

// TestParseSkippableEvents: the parser reports what makes an event unsuitable
// rather than deciding. Importing a cancelled or declined meeting as time
// worked would be inventing an hour.
func TestParseSkippableEvents(t *testing.T) {
	const file = `BEGIN:VCALENDAR
BEGIN:VEVENT
UID:cancelled
DTSTART:20260318T080000Z
DTEND:20260318T090000Z
STATUS:CANCELLED
SUMMARY:Cancelled meeting
END:VEVENT
BEGIN:VEVENT
UID:declined
DTSTART:20260318T100000Z
DTEND:20260318T110000Z
ATTENDEE;PARTSTAT=DECLINED;CN=Me:mailto:me@example.com
SUMMARY:Not going
END:VEVENT
BEGIN:VEVENT
UID:allday
DTSTART;VALUE=DATE:20260318
DTEND;VALUE=DATE:20260319
SUMMARY:Public holiday
END:VEVENT
BEGIN:VEVENT
UID:weekly
DTSTART:20260318T140000Z
DTEND:20260318T150000Z
RRULE:FREQ=WEEKLY;BYDAY=WE
SUMMARY:Weekly sync
END:VEVENT
END:VCALENDAR
`
	events, err := Parse(strings.NewReader(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	byUID := map[string]Event{}
	for _, event := range events {
		byUID[event.UID] = event
	}
	if !byUID["cancelled"].Cancelled {
		t.Error("a cancelled event was not marked")
	}
	if !byUID["declined"].Declined {
		t.Error("a declined event was not marked")
	}
	if !byUID["allday"].AllDay {
		t.Error("an all-day event was not marked")
	}
	// Only the first instance of a recurring event, flagged as such: expanding
	// an RRULE correctly needs the exception dates and the anchor zone, and
	// getting it subtly wrong invents meetings that never happened.
	if !byUID["weekly"].Recurring {
		t.Error("a recurring event was not marked")
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"PT1H":     time.Hour,
		"PT30M":    30 * time.Minute,
		"PT1H30M":  90 * time.Minute,
		"P1D":      24 * time.Hour,
		"P1DT2H":   26 * time.Hour,
		"PT45S":    45 * time.Second,
		"P1W":      7 * 24 * time.Hour,
		"-PT15M":   -15 * time.Minute,
		"PT0H0M0S": 0,
	}
	for text, want := range cases {
		got, err := parseDuration(text)
		if err != nil {
			t.Errorf("parseDuration(%q): %v", text, err)
			continue
		}
		if got != want {
			t.Errorf("parseDuration(%q) = %s, want %s", text, got, want)
		}
	}
	for _, bad := range []string{"1H", "banana", "PT1X"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) was accepted", bad)
		}
	}
}

// TestParseTruncatedFileIsAnError rather than a silently short list: a file cut
// off mid-event has lost data, and importing what survived would be worse than
// refusing.
func TestParseTruncatedFile(t *testing.T) {
	const file = `BEGIN:VCALENDAR
BEGIN:VEVENT
UID:truncated
DTSTART:20260318T080000Z
SUMMARY:Cut off here
`
	if _, err := Parse(strings.NewReader(file)); err == nil {
		t.Fatal("a truncated file was accepted")
	}
}

// TestParseHandlesBareLineFeeds. A file that has been through a mail client, or
// been edited, may not have CRLF line endings any more.
func TestParseHandlesBareLineFeeds(t *testing.T) {
	const file = "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:x\nDTSTART:20260318T080000Z\n" +
		"DTEND:20260318T090000Z\nSUMMARY:Plain newlines\nEND:VEVENT\nEND:VCALENDAR\n"

	events, err := Parse(strings.NewReader(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 || events[0].Summary != "Plain newlines" {
		t.Fatalf("got %+v", events)
	}
}

func TestUnescape(t *testing.T) {
	cases := map[string]string{
		`plain`:           `plain`,
		`a\,b`:            `a,b`,
		`a\;b`:            `a;b`,
		`line\nbreak`:     "line\nbreak",
		`back\\slash`:     `back\slash`,
		`trailing\`:       `trailing\`,
		`unknown\qescape`: `unknown\qescape`,
	}
	for input, want := range cases {
		if got := unescape(input); got != want {
			t.Errorf("unescape(%q) = %q, want %q", input, got, want)
		}
	}
}
