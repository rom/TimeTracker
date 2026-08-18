package export

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

// A minimal PDF writer.
//
// This exists because generating PDF in-process is a hard requirement
// (docs/adr/0007-pure-go-document-generation.md): the alternatives all mean
// spawning a browser or an office suite, which would break the single-binary
// promise and the sandbox that forbids executing anything else.
//
// The scope is deliberately narrow - what a timesheet report needs and nothing
// more: text in three weights, lines, filled rectangles, and page breaks. It
// uses the standard 14 fonts, which every conforming reader has built in, so
// nothing has to be embedded and the output stays small.
//
// What it does NOT do, and should not be extended to do without reconsidering
// the decision: images, transparency, colour spaces beyond DeviceRGB, embedded
// fonts, or anything requiring a cross-reference stream. If a report ever needs
// those, a real PDF library is the right answer.

// Page geometry, in PostScript points (1/72 inch). A4 rather than Letter,
// because the primary users are European; the difference matters only at the
// margins and both print acceptably on either.
const (
	pageWidth   = 595.28 // A4 width
	pageHeight  = 841.89 // A4 height
	marginLeft  = 42.0
	marginRight = 42.0
	marginTop   = 48.0
	marginBot   = 48.0

	contentWidth = pageWidth - marginLeft - marginRight
)

// Font names from the standard 14.
const (
	fontRegular = "Helvetica"
	fontBold    = "Helvetica-Bold"
	fontItalic  = "Helvetica-Oblique"
)

// pdfDoc accumulates pages.
type pdfDoc struct {
	pages []*pdfPage
	// current is the page being written to.
	current *pdfPage
	// y is the current vertical position, measured from the top of the page
	// downwards, which is the opposite of PDF's own coordinate system but is how
	// anyone laying out a document thinks.
	y float64
	// onNewPage draws the repeating header on each page after the first, which
	// is what makes a multi-page table readable.
	onNewPage func(d *pdfDoc)
}

// pdfPage is one page's content stream.
type pdfPage struct {
	content bytes.Buffer
}

// newPDF starts a document with one page.
func newPDF() *pdfDoc {
	d := &pdfDoc{}
	d.newPage()
	return d
}

// newPage starts a fresh page and runs the repeating-header callback.
func (d *pdfDoc) newPage() {
	page := &pdfPage{}
	d.pages = append(d.pages, page)
	d.current = page
	d.y = marginTop

	if d.onNewPage != nil && len(d.pages) > 1 {
		d.onNewPage(d)
	}
}

// space advances the cursor, starting a new page when the content would run off
// the bottom.
func (d *pdfDoc) space(height float64) {
	if d.y+height > pageHeight-marginBot {
		d.newPage()
	}
	d.y += height
}

// remaining reports how much vertical room is left on the current page.
func (d *pdfDoc) remaining() float64 {
	return pageHeight - marginBot - d.y
}

// text draws a line of text at an absolute x, using the cursor's y.
//
// The y is flipped here: PDF measures from the bottom of the page, and every
// caller thinks in terms of distance from the top.
func (d *pdfDoc) text(x float64, s string, font string, size float64, grey float64) {
	fmt.Fprintf(&d.current.content, "BT /%s %.1f Tf %.3f g %.2f %.2f Td (%s) Tj ET\n",
		fontKey(font), size, grey, x, pageHeight-d.y, escapePDFString(s))
}

// textRight draws text right-aligned at x, which is what a column of figures
// needs.
func (d *pdfDoc) textRight(x float64, s string, font string, size float64, grey float64) {
	d.text(x-textWidth(s, font, size), s, font, size, grey)
}

// line draws a horizontal rule.
func (d *pdfDoc) line(x1, x2 float64, grey float64, width float64) {
	fmt.Fprintf(&d.current.content, "%.3f G %.2f w %.2f %.2f m %.2f %.2f l S\n",
		grey, width, x1, pageHeight-d.y, x2, pageHeight-d.y)
}

// rect fills a rectangle, used for table header bands and total rows.
func (d *pdfDoc) rect(x, width, height float64, grey float64) {
	fmt.Fprintf(&d.current.content, "%.3f g %.2f %.2f %.2f %.2f re f\n",
		grey, x, pageHeight-d.y-height+3, width, height)
}

// escapePDFString escapes the three characters that are special inside a PDF
// literal string, and drops anything outside the font's encoding.
//
// WinAnsiEncoding covers Latin-1 plus the common typographic characters, which
// is enough for English and Swedish. A character outside it is replaced rather
// than emitted raw, because an unencodable byte produces a mojibake glyph in the
// reader rather than an error anyone would notice.
func escapePDFString(s string) string {
	var out strings.Builder
	for _, r := range s {
		switch r {
		case '(':
			out.WriteString(`\(`)
		case ')':
			out.WriteString(`\)`)
		case '\\':
			out.WriteString(`\\`)
		default:
			if b, ok := winAnsi(r); ok {
				if b < 32 || b > 126 {
					// Octal escape, which is how a PDF literal carries a byte
					// above ASCII.
					fmt.Fprintf(&out, `\%03o`, b)
				} else {
					out.WriteByte(b)
				}
			} else {
				out.WriteByte('?')
			}
		}
	}
	return out.String()
}

// winAnsi maps a rune to its WinAnsiEncoding byte.
//
// The mapping is Latin-1 for most of the range, with a handful of typographic
// characters in the 0x80-0x9F block that Latin-1 leaves as control codes. Only
// the ones this application actually emits are listed.
func winAnsi(r rune) (byte, bool) {
	switch {
	case r < 0x100 && r >= 0:
		// Latin-1 covers both English and Swedish, including å, ä, ö.
		return byte(r), true
	}
	switch r {
	case '–': // en dash, used between times
		return 0x96, true
	case '—': // em dash
		return 0x97, true
	case '‘':
		return 0x91, true
	case '’':
		return 0x92, true
	case '“':
		return 0x93, true
	case '”':
		return 0x94, true
	case '•': // bullet
		return 0x95, true
	case '€': // euro
		return 0x80, true
	case '−': // minus sign, which the Swedish formatter emits
		return '-', true
	case ' ': // non-breaking space, the Swedish group separator
		return ' ', true
	}
	return 0, false
}

// fontKey maps a font name to the resource key used in the content stream.
func fontKey(name string) string {
	switch name {
	case fontBold:
		return "F2"
	case fontItalic:
		return "F3"
	default:
		return "F1"
	}
}

// render writes the finished PDF.
//
// The structure is the simplest that conforms: a catalogue, a page tree, one
// content stream per page, a shared font resource dictionary, and a classic
// cross-reference table. No compression - a timesheet is a few kilobytes of
// text, and an uncompressed file is inspectable with a text editor when
// something goes wrong.
func (d *pdfDoc) render(w io.Writer) error {
	var buf bytes.Buffer
	// offsets[n] is the byte offset of object n, for the cross-reference table.
	offsets := map[int]int{}

	object := func(number int, body string) {
		offsets[number] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", number, body)
	}

	buf.WriteString("%PDF-1.4\n")
	// A comment with high bytes, which tells any tool handling the file that it
	// is binary and must not be line-ending-converted.
	buf.WriteString("%\xE2\xE3\xCF\xD3\n")

	// Object numbering: 1 catalogue, 2 page tree, 3 font dictionary,
	// 4..(3+n) pages, then the content streams.
	pageCount := len(d.pages)
	firstPageObj := 4
	firstContentObj := firstPageObj + pageCount

	object(1, "<< /Type /Catalog /Pages 2 0 R >>")

	var kids strings.Builder
	for i := range d.pages {
		fmt.Fprintf(&kids, "%d 0 R ", firstPageObj+i)
	}
	object(2, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>",
		pageCount, strings.TrimSpace(kids.String())))

	object(3, fmt.Sprintf(
		"<< /F1 << /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >>\n"+
			"   /F2 << /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >>\n"+
			"   /F3 << /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >> >>",
		fontRegular, fontBold, fontItalic))

	for i := range d.pages {
		object(firstPageObj+i, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
				"/Resources << /Font 3 0 R >> /Contents %d 0 R >>",
			pageWidth, pageHeight, firstContentObj+i))
	}

	for i, page := range d.pages {
		content := page.content.String()
		object(firstContentObj+i, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream",
			len(content), content))
	}

	// The cross-reference table, which must list every object in order.
	total := firstContentObj + pageCount
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", total)
	buf.WriteString("0000000000 65535 f \n")

	numbers := make([]int, 0, len(offsets))
	for n := range offsets {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	for _, n := range numbers {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}

	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		total, xrefOffset)

	_, err := w.Write(buf.Bytes())
	return err
}

// textWidth estimates the rendered width of a string.
//
// Helvetica's real metrics are a table of 315 widths per font; carrying them
// would be more code than the rest of this file. These averages are accurate
// enough for right-aligning a column of figures and for deciding where to
// truncate a long note - the two things width is used for here - and a few
// points of error is invisible in either.
func textWidth(s string, font string, size float64) float64 {
	perChar := 0.50 // Helvetica's average, as a fraction of the point size
	if font == fontBold {
		perChar = 0.55
	}

	width := 0.0
	for _, r := range s {
		switch {
		case r == ' ':
			width += 0.28
		case r == 'i' || r == 'l' || r == 'j' || r == '.' || r == ',' || r == ':':
			width += 0.24
		case r == 'm' || r == 'w' || r == 'M' || r == 'W':
			width += 0.83
		case r >= '0' && r <= '9':
			// Digits are tabular in Helvetica: all the same width, which is why
			// a column of figures aligns.
			width += 0.556
		default:
			width += perChar
		}
	}
	return width * size
}

// truncate shortens a string to fit a width, ending with an ellipsis.
func truncateToWidth(s string, font string, size, maxWidth float64) string {
	if textWidth(s, font, size) <= maxWidth {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "..."
		if textWidth(candidate, font, size) <= maxWidth {
			return candidate
		}
	}
	return ""
}
