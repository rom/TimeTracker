package export

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/rom/timetracker/internal/domain"
)

// WriteDOCX renders a report as an Office Open XML document.
//
// A .docx is a ZIP containing a handful of XML parts, so this needs nothing
// beyond archive/zip and encoding/xml - no dependency, no subprocess, and no
// LibreOffice to install (docs/adr/0007-pure-go-document-generation.md).
//
// The parts written here are the minimum a conforming reader requires:
//
//	[Content_Types].xml    declares the type of every part
//	_rels/.rels            points at the main document
//	word/document.xml      the content itself
//	word/styles.xml        the named styles the content refers to
//
// Word is stricter than LibreOffice about several of these, so the output is
// checked against both before release (docs/TEST.md §5).

// WriteDOCX writes the report as a Word document.
func WriteDOCX(w io.Writer, report Report) error {
	archive := zip.NewWriter(w)

	parts := []struct {
		name    string
		content string
	}{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"word/styles.xml", stylesXML},
		{"word/document.xml", documentXML(report)},
	}

	for _, part := range parts {
		writer, err := archive.Create(part.name)
		if err != nil {
			return fmt.Errorf("create %s: %w", part.name, err)
		}
		if _, err := io.WriteString(writer, part.content); err != nil {
			return fmt.Errorf("write %s: %w", part.name, err)
		}
	}

	if err := archive.Close(); err != nil {
		return fmt.Errorf("finish DOCX: %w", err)
	}
	return nil
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

// stylesXML defines the named styles the document refers to.
//
// Defining them rather than formatting inline is what makes the output editable:
// someone dropping this into their own letterhead can restyle Heading1 once
// instead of reformatting every paragraph.
const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:docDefaults>
    <w:rPrDefault><w:rPr>
      <w:rFonts w:ascii="Calibri" w:hAnsi="Calibri"/>
      <w:sz w:val="20"/>
    </w:rPr></w:rPrDefault>
  </w:docDefaults>
  <w:style w:type="paragraph" w:styleId="Title">
    <w:name w:val="Title"/>
    <w:pPr><w:spacing w:after="120"/></w:pPr>
    <w:rPr><w:b/><w:sz w:val="40"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading1">
    <w:name w:val="heading 1"/>
    <w:pPr><w:spacing w:before="240" w:after="80"/><w:outlineLvl w:val="0"/></w:pPr>
    <w:rPr><w:b/><w:sz w:val="24"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Meta">
    <w:name w:val="Meta"/>
    <w:rPr><w:color w:val="595959"/></w:rPr>
  </w:style>
  <w:style w:type="table" w:styleId="TimesheetTable">
    <w:name w:val="Timesheet Table"/>
    <w:tblPr>
      <w:tblBorders>
        <w:top w:val="single" w:sz="4" w:color="BFBFBF"/>
        <w:bottom w:val="single" w:sz="4" w:color="BFBFBF"/>
        <w:insideH w:val="single" w:sz="4" w:color="D9D9D9"/>
      </w:tblBorders>
    </w:tblPr>
  </w:style>
</w:styles>`

// documentXML builds the body.
func documentXML(report Report) string {
	var body strings.Builder

	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)

	paragraph(&body, "Title", report.Title)

	period := fmt.Sprintf("%s to %s",
		report.From.Format("2006-01-02"), report.To.AddDate(0, 0, -1).Format("2006-01-02"))
	paragraph(&body, "Meta", period)
	if report.User != "" {
		paragraph(&body, "Meta", report.User)
	}
	paragraph(&body, "Meta", "Generated "+report.GeneratedAt.Format("2006-01-02 15:04")+" UTC")

	showAmounts := len(report.Totals.AmountsByCurrency) > 0

	for _, group := range groupLines(report.Lines) {
		paragraph(&body, "Heading1", group.label)

		body.WriteString(`<w:tbl>`)
		body.WriteString(`<w:tblPr><w:tblStyle w:val="TimesheetTable"/>` +
			`<w:tblW w:w="5000" w:type="pct"/></w:tblPr>`)

		headers := []string{"Date", "Assignment", "Description", "Hours"}
		if showAmounts {
			headers = append(headers, "Amount")
		}
		tableRow(&body, headers, true)

		for _, line := range group.lines {
			note := line.Note
			if line.Status == string(domain.StatusPending) {
				note = "(pending) " + note
			}
			cells := []string{
				line.Date.Format("2006-01-02"),
				line.Assignment,
				note,
				domain.FormatDecimalHours(line.Seconds),
			}
			if showAmounts {
				amount := ""
				if line.AmountMinor != 0 {
					amount = domain.NewMoney(line.AmountMinor, line.Currency).Decimal()
				}
				cells = append(cells, amount)
			}
			tableRow(&body, cells, false)
		}

		// The subtotal row, in bold.
		subtotal := []string{"", "", "Subtotal", domain.FormatDecimalHours(group.seconds)}
		if showAmounts {
			amount := ""
			for _, currency := range sortedCurrencies(group.amounts) {
				amount = domain.NewMoney(group.amounts[currency], currency).Decimal() + " " + currency
			}
			subtotal = append(subtotal, amount)
		}
		tableRow(&body, subtotal, true)

		body.WriteString(`</w:tbl>`)
	}

	paragraph(&body, "Heading1", "Totals")
	paragraph(&body, "", "Tracked: "+domain.FormatDecimalHours(report.Totals.SummedSeconds)+" h")
	if report.Totals.BillableSeconds != report.Totals.SummedSeconds {
		paragraph(&body, "", "Billable: "+domain.FormatDecimalHours(report.Totals.BillableSeconds)+" h")
	}
	if report.Totals.OverlapSeconds > 0 {
		// Stated, not hidden: a client whose invoice shows more hours than the
		// day contains is owed the explanation up front.
		paragraph(&body, "Meta", "Includes "+
			domain.FormatDecimalHours(report.Totals.OverlapSeconds)+
			" h of parallel work (several tasks running at once)")
	}
	for _, currency := range sortedCurrencies(report.Totals.AmountsByCurrency) {
		paragraph(&body, "", "Total "+currency+": "+
			domain.NewMoney(report.Totals.AmountsByCurrency[currency], currency).Decimal())
	}

	// The section properties must be the last element in the body; a document
	// without them opens with unpredictable page setup.
	body.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>` +
		`<w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/></w:sectPr>`)
	body.WriteString(`</w:body></w:document>`)

	return body.String()
}

// paragraph writes one styled paragraph.
func paragraph(out *strings.Builder, style, text string) {
	out.WriteString(`<w:p>`)
	if style != "" {
		fmt.Fprintf(out, `<w:pPr><w:pStyle w:val="%s"/></w:pPr>`, style)
	}
	fmt.Fprintf(out, `<w:r><w:t xml:space="preserve">%s</w:t></w:r>`, escapeXML(text))
	out.WriteString(`</w:p>`)
}

// tableRow writes one row, optionally in bold.
func tableRow(out *strings.Builder, cells []string, bold bool) {
	out.WriteString(`<w:tr>`)
	for _, cell := range cells {
		out.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr><w:p><w:r>`)
		if bold {
			out.WriteString(`<w:rPr><w:b/></w:rPr>`)
		}
		fmt.Fprintf(out, `<w:t xml:space="preserve">%s</w:t>`, escapeXML(cell))
		out.WriteString(`</w:r></w:p></w:tc>`)
	}
	out.WriteString(`</w:tr>`)
}

// escapeXML escapes text for an XML text node.
//
// encoding/xml's escaper is used rather than a hand-rolled one, because the set
// of characters that must be escaped in XML is larger and less obvious than it
// looks - and a document that fails to open is a worse failure than one that
// looks slightly wrong.
func escapeXML(s string) string {
	// Control characters are illegal in XML 1.0 at any escaping, so they are
	// dropped rather than escaped. A note pasted from a terminal can contain
	// them, and one such character makes the whole document unopenable.
	cleaned := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)

	var out strings.Builder
	if err := xml.EscapeText(&out, []byte(cleaned)); err != nil {
		return ""
	}
	return out.String()
}
