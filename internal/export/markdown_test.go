package export

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/domain"
)

func TestMarkdownStructure(t *testing.T) {
	report := sampleReport(t)

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"# Time report",
		"**Period:**",
		"**For:** Test User",
		"## Acme AB / Migration",
		"## Beta Ltd / Retainer",
		"## Totals",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the document is missing %q", want)
		}
	}

	// Every entry appears, including the pending one - it is listed even though
	// it counts for nothing.
	for _, line := range report.Lines {
		if !strings.Contains(out, line.Assignment) {
			t.Errorf("assignment %q is missing from the document", line.Assignment)
		}
	}
	if !strings.Contains(out, "(pending)") {
		t.Error("a pending entry must be marked as such, or the totals look wrong")
	}
}

// TestMarkdownTablesAreWellFormed is the property the format lives or dies by:
// every row of a table has the same number of cells as its header, or a reader
// renders a mangled table.
func TestMarkdownTablesAreWellFormed(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, sampleReport(t)); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	var columns, tables int
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "|") {
			// A blank line ends a table.
			columns = 0
			continue
		}
		cells := strings.Count(line, "|") - 1
		if columns == 0 {
			columns = cells
			tables++
			continue
		}
		if cells != columns {
			t.Errorf("row %q has %d cells, the header had %d", line, cells, columns)
		}
	}
	if tables == 0 {
		t.Fatal("no tables were produced at all")
	}
}

// TestMarkdownEscapesCellContent guards the case that makes a table fall apart:
// a customer or a note containing the character that separates cells.
func TestMarkdownEscapesCellContent(t *testing.T) {
	report := sampleReport(t)
	report.Lines[0].Note = "pipe | inside\nand a newline"
	report.Lines[0].Assignment = "Some *emphasis* and _more_"

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "pipe | inside") {
		t.Error("an unescaped pipe in a note splits the row into two cells")
	}
	if !strings.Contains(out, `pipe \| inside`) {
		t.Error("the pipe should survive as escaped text rather than disappear")
	}
	// The newline must not have ended the row.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "and a newline") && !strings.HasPrefix(line, "|") {
			t.Errorf("a newline in a note escaped its table row: %q", line)
		}
	}
	if !strings.Contains(out, `\*emphasis\*`) {
		t.Error("asterisks in an assignment name should not become emphasis")
	}
}

// TestMarkdownPadsByRuneNotByte is why displayWidth exists: padding a table by
// byte length makes every column containing a non-ASCII name ragged.
func TestMarkdownPadsByRuneNotByte(t *testing.T) {
	report := sampleReport(t)
	report.Lines[0].Assignment = "Björk"
	report.Lines[1].Assignment = "Björk"

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	// Within one table every row must be the same rune width.
	var width int
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "|") {
			width = 0
			continue
		}
		runes := len([]rune(line))
		if width == 0 {
			width = runes
			continue
		}
		if runes != width {
			t.Errorf("row %q is %d columns wide, the header was %d", line, runes, width)
		}
	}
}

// TestMarkdownTotalsMatchTheReport is the same guarantee the other formats give:
// one Report in, and no format may disagree with another about the figures.
func TestMarkdownTotalsMatchTheReport(t *testing.T) {
	report := sampleReport(t)

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	out := buf.String()

	tracked := domain.FormatDecimalHours(report.Totals.SummedSeconds)
	if !strings.Contains(out, "**Tracked:** "+tracked+" h") {
		t.Errorf("the tracked total %q does not appear in the document", tracked)
	}
	// The overlap in the sample is real, and has to be stated rather than left
	// for a client to notice.
	if report.Totals.OverlapSeconds > 0 &&
		!strings.Contains(out, "**Parallel work:**") {
		t.Error("an overlapping day must explain itself in the document")
	}
	for currency, amount := range report.Totals.AmountsByCurrency {
		want := domain.NewMoney(amount, currency).Decimal()
		if !strings.Contains(out, "**Total "+currency+":** "+want) {
			t.Errorf("the %s total %q is missing", currency, want)
		}
	}
}

// TestEveryFormatWrites is the check that keeps the offered list and the
// dispatch in step. A format in Formats with no writer behind it reaches a user
// as an empty download, which is the one failure nobody reports as a bug.
func TestEveryFormatWrites(t *testing.T) {
	report := sampleReport(t)

	for _, format := range Formats {
		t.Run(string(format), func(t *testing.T) {
			if !format.Known() {
				t.Fatalf("%s is offered but Known reports it is not", format)
			}
			var buf bytes.Buffer
			if err := format.Write(&buf, report); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if buf.Len() == 0 {
				t.Fatal("wrote nothing at all")
			}
			if format.ContentType() == "application/octet-stream" {
				t.Errorf("%s has no content type of its own", format)
			}
			if format.Label() == "" {
				t.Errorf("%s would render a button with no text on it", format)
			}
		})
	}
}

func TestUnknownFormatIsRefused(t *testing.T) {
	var buf bytes.Buffer
	err := Format("xlsx").Write(&buf, sampleReport(t))
	if err == nil {
		t.Fatal("an unknown format must not write anything successfully")
	}
	if Format("xlsx").Known() {
		t.Error("Known must not claim an unwritable format")
	}
}
