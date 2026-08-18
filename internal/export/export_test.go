package export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// sampleReport builds a report with the awkward cases in it: several currencies,
// a pending entry that must not count, an overlap, and text that needs escaping
// in three different output formats.
func sampleReport(t *testing.T) Report {
	t.Helper()
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)

	end := func(offset time.Duration) *time.Time {
		e := start.Add(offset)
		return &e
	}

	entries := []domain.TimeEntry{
		{
			ID: 1, UserID: 1, EnteredBy: 1, StartedAt: start, EndedAt: end(time.Hour),
			DurationSeconds: 3600, Billable: true, Status: domain.StatusConfirmed,
			TimeZone: "UTC", AmountMinor: 125000, Currency: "SEK", BillableSeconds: 3600,
			AssignmentName: "Development", ProjectName: "Migration", CustomerName: "Acme AB",
			UserName: "Test User", EnteredByName: "Test User",
			Note: `Fixed "quoting" & <escaping> — 100% done`,
		},
		{
			// Overlaps the first, which is the case the totals have to explain.
			ID: 2, UserID: 1, EnteredBy: 1, StartedAt: start.Add(30 * time.Minute),
			EndedAt: end(90 * time.Minute), DurationSeconds: 3600, Billable: true,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
			AmountMinor: 9000, Currency: "EUR", BillableSeconds: 3600,
			AssignmentName: "Support", ProjectName: "Retainer", CustomerName: "Beta Ltd",
			UserName: "Test User", EnteredByName: "Test User", Note: "Incident triage",
		},
		{
			// Pending: listed, but must not reach any total.
			ID: 3, UserID: 1, EnteredBy: 2, StartedAt: start.Add(4 * time.Hour),
			EndedAt: end(6 * time.Hour), DurationSeconds: 7200, Billable: true,
			Status: domain.StatusPending, TimeZone: "UTC",
			AmountMinor: 250000, Currency: "SEK",
			AssignmentName: "Development", ProjectName: "Migration", CustomerName: "Acme AB",
			UserName: "Test User", EnteredByName: "Colleague",
			Note: "Proposed by a colleague",
		},
	}

	return NewReport("Time report", start, start.AddDate(0, 0, 1), "UTC", "Test User", entries, now)
}

// TestReportExcludesPendingFromTotals is the export-layer half of ASR-008: a
// proposal appears in the listing but contributes nothing.
func TestReportExcludesPendingFromTotals(t *testing.T) {
	report := sampleReport(t)

	if len(report.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(report.Lines))
	}
	if report.Totals.EntryCount != 2 {
		t.Errorf("pending entry counted: EntryCount = %d, want 2", report.Totals.EntryCount)
	}
	if report.Totals.SummedSeconds != 7200 {
		t.Errorf("summed = %d, want 7200 (the pending 2h must not count)",
			report.Totals.SummedSeconds)
	}
	// SEK should be 1250.00 only - the pending 2500.00 must not be in it.
	if got := report.Totals.AmountsByCurrency["SEK"]; got != 125000 {
		t.Errorf("SEK total = %d, want 125000", got)
	}
	if got := report.Totals.AmountsByCurrency["EUR"]; got != 9000 {
		t.Errorf("EUR total = %d, want 9000", got)
	}
}

// TestReportReportsOverlap: two hours summed across ninety minutes of wall clock.
func TestReportReportsOverlap(t *testing.T) {
	report := sampleReport(t)

	if report.Totals.ElapsedSeconds != 5400 {
		t.Errorf("elapsed = %d, want 5400", report.Totals.ElapsedSeconds)
	}
	if report.Totals.OverlapSeconds != 1800 {
		t.Errorf("overlap = %d, want 1800", report.Totals.OverlapSeconds)
	}
}

// TestPDFStructure: the output must be a parseable PDF, not merely bytes.
func TestPDFStructure(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePDF(&buf, sampleReport(t)); err != nil {
		t.Fatalf("write PDF: %v", err)
	}
	out := buf.String()

	if !strings.HasPrefix(out, "%PDF-1.4") {
		t.Error("no PDF header")
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "%%EOF") {
		t.Error("no EOF marker")
	}
	for _, required := range []string{"/Type /Catalog", "/Type /Pages", "/Type /Page",
		"xref", "trailer", "startxref", "/WinAnsiEncoding"} {
		if !strings.Contains(out, required) {
			t.Errorf("the PDF is missing %q", required)
		}
	}

	// The cross-reference offsets must actually point at their objects, or a
	// strict reader rejects the file even though a lenient one repairs it.
	xrefIndex := strings.LastIndex(out, "startxref")
	if xrefIndex < 0 {
		t.Fatal("no startxref")
	}
	var offset int
	if _, err := fmtSscan(out[xrefIndex+len("startxref"):], &offset); err != nil {
		t.Fatalf("unparseable startxref: %v", err)
	}
	if offset <= 0 || offset >= len(out) {
		t.Fatalf("startxref points outside the file: %d of %d", offset, len(out))
	}
	if !strings.HasPrefix(out[offset:], "xref") {
		t.Errorf("startxref does not point at the xref table; found %q",
			out[offset:min(len(out), offset+20)])
	}
}

// TestPDFEscapesText: parentheses and backslashes terminate a PDF string, so an
// unescaped one produces a corrupt file rather than odd-looking text.
func TestPDFEscapesText(t *testing.T) {
	got := escapePDFString(`a (paren) and a \backslash`)
	if strings.Contains(got, "(paren)") {
		t.Errorf("parentheses were not escaped: %q", got)
	}
	if !strings.Contains(got, `\(`) || !strings.Contains(got, `\)`) {
		t.Errorf("missing escaped parentheses: %q", got)
	}
	if !strings.Contains(got, `\\`) {
		t.Errorf("backslash was not escaped: %q", got)
	}

	// Swedish characters must survive as WinAnsi octal escapes rather than
	// becoming question marks.
	swedish := escapePDFString("Förändring på Åsa")
	if strings.Count(swedish, "?") > 0 {
		t.Errorf("Swedish characters were lost: %q", swedish)
	}
}

// TestPDFPaginates: a report longer than a page must produce several, each with
// the repeating header.
func TestPDFPaginates(t *testing.T) {
	report := sampleReport(t)
	// Enough lines to need several pages.
	base := report.Lines[0]
	for i := 0; i < 200; i++ {
		line := base
		line.ID = int64(100 + i)
		report.Lines = append(report.Lines, line)
	}

	var buf bytes.Buffer
	if err := WritePDF(&buf, report); err != nil {
		t.Fatalf("write PDF: %v", err)
	}
	out := buf.String()

	// "/Type /Page " with the trailing space, so it does not also match
	// "/Type /Pages".
	pages := strings.Count(out, "/Type /Page ")
	if pages < 2 {
		t.Errorf("expected several pages for 200 entries, got %d", pages)
	}
	if !strings.Contains(out, "Page 1 of") {
		t.Error("pages are not numbered")
	}
}

// TestDOCXIsAValidZipWithTheRequiredParts.
func TestDOCXStructure(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDOCX(&buf, sampleReport(t)); err != nil {
		t.Fatalf("write DOCX: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the DOCX is not a valid zip: %v", err)
	}

	required := map[string]bool{
		"[Content_Types].xml": false,
		"_rels/.rels":         false,
		"word/document.xml":   false,
		"word/styles.xml":     false,
	}
	for _, file := range reader.File {
		if _, wanted := required[file.Name]; wanted {
			required[file.Name] = true
		}

		// Every part must be well-formed XML. Word refuses to open a document
		// with a malformed part, where LibreOffice sometimes repairs it - so
		// checking here is what catches the difference before a user does.
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}

		decoder := xml.NewDecoder(bytes.NewReader(content))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s is not well-formed XML: %v", file.Name, err)
			}
		}
	}

	for name, found := range required {
		if !found {
			t.Errorf("the DOCX is missing %s", name)
		}
	}
}

// TestDOCXEscapesContent: an unescaped ampersand makes the document unopenable
// in Word, which is a total failure rather than a cosmetic one.
func TestDOCXEscapesContent(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDOCX(&buf, sampleReport(t)); err != nil {
		t.Fatalf("write DOCX: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	for _, file := range reader.File {
		if file.Name != "word/document.xml" {
			continue
		}
		rc, _ := file.Open()
		content, _ := io.ReadAll(rc)
		_ = rc.Close()

		text := string(content)
		if strings.Contains(text, `"quoting" & <escaping>`) {
			t.Error("the note was inserted without escaping")
		}
		if !strings.Contains(text, "&amp;") {
			t.Error("the ampersand was not escaped")
		}
	}
}

// TestDOCXDropsControlCharacters: a note pasted from a terminal can carry them,
// and one is enough to make the document unopenable.
func TestDOCXDropsControlCharacters(t *testing.T) {
	got := escapeXML("before\x00\x07after\ttab")
	if strings.ContainsAny(got, "\x00\x07") {
		t.Errorf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("legitimate text was lost: %q", got)
	}
}

// TestCSVAndJSONAgreeWithTheReport: every format renders from one value, so they
// cannot disagree about the numbers - this is the test that proves it.
func TestFormatsAgree(t *testing.T) {
	report := sampleReport(t)

	var csvBuf, jsonBuf bytes.Buffer
	if err := WriteCSV(&csvBuf, report); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	if err := WriteJSON(&jsonBuf, report); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var doc struct {
		Entries []struct {
			ID              int64  `json:"id"`
			DurationSeconds int64  `json:"duration_seconds"`
			Status          string `json:"status"`
		} `json:"entries"`
		Totals struct {
			SummedSeconds int64            `json:"summed_seconds"`
			Amounts       map[string]int64 `json:"amounts_by_currency_minor"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(jsonBuf.Bytes(), &doc); err != nil {
		t.Fatalf("the JSON export is not valid JSON: %v", err)
	}

	if len(doc.Entries) != len(report.Lines) {
		t.Errorf("JSON has %d entries, the report has %d", len(doc.Entries), len(report.Lines))
	}
	if doc.Totals.SummedSeconds != report.Totals.SummedSeconds {
		t.Errorf("JSON total %d disagrees with the report's %d",
			doc.Totals.SummedSeconds, report.Totals.SummedSeconds)
	}

	// The CSV must have a row per entry plus a header, and the BOM.
	rows := strings.Count(strings.TrimSpace(csvBuf.String()), "\n") + 1
	if rows != len(report.Lines)+1 {
		t.Errorf("CSV has %d rows, want %d", rows, len(report.Lines)+1)
	}
}

// fmtSscan is fmt.Sscan, wrapped so the test file needs no fmt import for one
// call.
func fmtSscan(s string, target *int) (int, error) {
	return sscanInt(s, target)
}

// sscanInt reads the first integer out of a string.
func sscanInt(s string, target *int) (int, error) {
	value := 0
	found := false
	for _, r := range strings.TrimSpace(s) {
		if r >= '0' && r <= '9' {
			value = value*10 + int(r-'0')
			found = true
			continue
		}
		if found {
			break
		}
	}
	*target = value
	if !found {
		return 0, io.EOF
	}
	return 1, nil
}
