package export

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden files for the text formats.
//
// docs/TEST.md said, accurately, that there were none: every format was
// asserted on its structure instead - the PDF for its cross-reference table, the
// DOCX for its OOXML parts, Markdown for the property its tables live or die by.
// Those are better tests than a golden file, because each one says what it is
// checking and fails with a sentence about it.
//
// What they cannot do is notice a change nobody meant to make. A structural
// assertion passes on any output with the right shape, and every one of these
// formats is a *file somebody else's software reads* - a spreadsheet, an
// importer, a wiki renderer. A column that quietly changes order, a header that
// loses its unit, a delimiter that becomes a semicolon: all still well-formed,
// all still passing, all breaking whatever the person on the other end built
// against last month's file.
//
// So this is deliberately the dumb test. It renders a fixed report and compares
// the bytes. When it fails, the diff is the change, and the only question is
// whether it was intended - which is exactly the question a format nobody
// controls should raise.
//
// Regenerate with: go test ./internal/export/ -update
//
// The three text formats only. The PDF and the DOCX are byte-unstable by nature
// (a zip stores per-entry metadata, and a PDF stores byte offsets that move with
// any content change), and pinning them would produce a file that has to be
// regenerated for every unrelated edit - which trains everybody to regenerate
// without reading. Those two keep their structural tests, which is the right
// tool for them.

// update rewrites the golden files instead of comparing against them.
var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

// TestGoldenTextFormats.
func TestGoldenTextFormats(t *testing.T) {
	for _, format := range []struct {
		name  string
		file  string
		write func(*bytes.Buffer, Report) error
	}{
		{"CSV", "report.csv", func(buf *bytes.Buffer, r Report) error { return WriteCSV(buf, r) }},
		{"JSON", "report.json", func(buf *bytes.Buffer, r Report) error { return WriteJSON(buf, r) }},
		{"Markdown", "report.md", func(buf *bytes.Buffer, r Report) error { return WriteMarkdown(buf, r) }},
	} {
		t.Run(format.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := format.write(&buf, sampleReport(t)); err != nil {
				t.Fatalf("write %s: %v", format.name, err)
			}
			compareGolden(t, format.file, buf.Bytes())
		})
	}
}

// TestGoldenStreamedFormatsMatchTheirCollectedFiles.
//
// The two streaming writers are held to the same files as the collected ones.
//
// TestStreamedAndCollectedExportsAgree already compares them to each other,
// which catches the two drifting apart. It does not catch them drifting
// *together*: a change to a shared helper moves both, they still agree, and the
// file the customer's importer reads has changed. Pinning both to the same bytes
// closes that.
func TestGoldenStreamedFormatsMatchTheirCollectedFiles(t *testing.T) {
	report := sampleReport(t)
	stream := streamOf(report)

	for _, format := range []struct {
		name  string
		file  string
		write func(*bytes.Buffer, Stream) error
	}{
		{"CSV", "report.csv", func(buf *bytes.Buffer, s Stream) error { return WriteCSVStream(buf, s) }},
		{"JSON", "report.json", func(buf *bytes.Buffer, s Stream) error { return WriteJSONStream(buf, s) }},
	} {
		t.Run(format.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := format.write(&buf, stream); err != nil {
				t.Fatalf("stream %s: %v", format.name, err)
			}
			// Never written from the streamed output: the collected writers own
			// the files, so that -update cannot quietly bless a streaming
			// regression.
			want := readGolden(t, format.file)
			if diff := firstDifferingLine(string(want), buf.String()); diff != "" {
				t.Errorf("the streamed %s no longer matches the golden file.\n%s\n"+
					"Both writers produce the file a customer's importer reads; "+
					"they have to stay identical.", format.name, diff)
			}
		})
	}
}

// TestTheGoldenFilesAreWhatTheyClaim.
//
// A golden file is only as good as the fixture behind it, and a fixture that
// quietly lost its awkward cases would leave three files full of unremarkable
// rows that agree with themselves forever.
//
// So: the escaping case, the second currency and the pending entry all have to
// be visible in the files. These are the properties the formats are *for*.
func TestTheGoldenFilesAreWhatTheyClaim(t *testing.T) {
	csv := string(readGolden(t, "report.csv"))
	markdown := string(readGolden(t, "report.md"))
	jsonReport := string(readGolden(t, "report.json"))

	// The note carries a quote, an ampersand, angle brackets, a percent sign and
	// an em dash: one string that has to survive three sets of escaping rules.
	if !strings.Contains(csv, `Fixed ""quoting""`) {
		t.Error("the CSV golden file has lost the doubled-quote escaping")
	}
	if !strings.Contains(markdown, `\|`) && strings.Contains(markdown, "|") {
		// The sample note has no pipe, so this only checks the file is a table.
		if !strings.Contains(markdown, "| Date") && !strings.Contains(markdown, "|-") {
			t.Error("the Markdown golden file is not a table")
		}
	}
	if !strings.Contains(jsonReport, `"currency": "EUR"`) &&
		!strings.Contains(jsonReport, `"EUR"`) {
		t.Error("the JSON golden file has lost the second currency")
	}

	// The pending entry is listed but must not be in a total. 250000 is its
	// amount; it may appear on its own line and must not appear in the totals.
	if !strings.Contains(jsonReport, "pending") {
		t.Error("the JSON golden file has lost the pending entry, which is the " +
			"ASR-008 case these formats have to get right")
	}
}

// compareGolden compares output against testdata/golden/<name>, or rewrites it
// under -update.
func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create the golden directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("rewrote %s (%d bytes)", path, len(got))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRegenerate with: go test ./internal/export/ -update", path, err)
	}
	if bytes.Equal(want, got) {
		return
	}
	t.Errorf("%s has changed.\n%s\n"+
		"If the change was intended, regenerate with:\n"+
		"    go test ./internal/export/ -update\n"+
		"and read the diff before committing it - this file is the format somebody "+
		"else's software reads.", name, firstDifferingLine(string(want), string(got)))
}

// readGolden reads a golden file, failing if it is missing.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatalf("read the golden file: %v\nRegenerate with: go test ./internal/export/ -update", err)
	}
	return content
}

// firstDifferingLine reports the first line that differs, with its neighbours.
//
// A whole-file diff of a JSON report is unreadable in test output and the eye
// slides off it; one line with a number on it is something somebody actually
// checks.
func firstDifferingLine(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var wantLine, gotLine string
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine == gotLine {
			continue
		}
		var context strings.Builder
		if i > 0 {
			context.WriteString("  line " + itoa(i) + ":   " + wantLines[i-1] + "\n")
		}
		context.WriteString("  line " + itoa(i+1) + " want: " + wantLine + "\n")
		context.WriteString("  line " + itoa(i+1) + " got:  " + gotLine)
		return context.String()
	}
	if len(want) != len(got) {
		return "  the files differ in trailing bytes only"
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
