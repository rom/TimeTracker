package export

import (
	"fmt"
	"io"
	"sort"

	"github.com/rom/timetracker/internal/domain"
)

// WritePDF renders a report as a client-presentable PDF.
//
// The layout is a title block, a metadata line, the entries grouped by customer
// and project with a repeating table header on every page, then totals. It is
// deliberately plain: this is a document someone sends to a client alongside an
// invoice, and a timesheet that looks designed is a timesheet that looks like it
// is hiding something.

// Column positions, measured from the left margin.
var pdfColumns = struct {
	date, assignment, note, hours, amount float64
}{
	date:       marginLeft,
	assignment: marginLeft + 62,
	note:       marginLeft + 190,
	hours:      marginLeft + contentWidth - 120, // right edge of the hours column
	amount:     marginLeft + contentWidth,       // right edge of the amount column
}

func WritePDF(w io.Writer, report Report) error {
	doc := newPDF()

	// The repeating header: drawn on every page after the first, so a table that
	// runs over is still readable. Without it page two is a wall of unlabelled
	// figures.
	doc.onNewPage = func(d *pdfDoc) {
		d.text(marginLeft, report.Title, fontBold, 9, 0.4)
		d.space(16)
		drawPDFTableHead(d, report)
	}

	// ---- title block -------------------------------------------------------

	doc.space(4)
	doc.text(marginLeft, report.Title, fontBold, 18, 0)
	doc.space(20)

	period := fmt.Sprintf("%s to %s",
		report.From.Format("2006-01-02"),
		report.To.AddDate(0, 0, -1).Format("2006-01-02"))
	doc.text(marginLeft, period, fontRegular, 10, 0.35)
	doc.space(13)

	if report.User != "" {
		doc.text(marginLeft, report.User, fontRegular, 10, 0.35)
		doc.space(13)
	}
	doc.text(marginLeft,
		"Generated "+report.GeneratedAt.Format("2006-01-02 15:04")+" UTC",
		fontRegular, 8, 0.5)
	doc.space(18)

	doc.line(marginLeft, marginLeft+contentWidth, 0.7, 0.8)
	doc.space(16)

	// ---- entries, grouped --------------------------------------------------

	drawPDFTableHead(doc, report)

	groups := groupLines(report.Lines)
	for _, group := range groups {
		// Keep a group heading with at least one of its rows: a heading alone at
		// the foot of a page is the classic ugly break.
		if doc.remaining() < 46 {
			doc.newPage()
		}

		doc.space(6)
		doc.text(marginLeft, group.label, fontBold, 9.5, 0.15)
		doc.space(12)

		for _, line := range group.lines {
			if doc.remaining() < 14 {
				doc.newPage()
			}

			doc.text(pdfColumns.date, line.Date.Format("2006-01-02"), fontRegular, 8.5, 0.25)
			doc.text(pdfColumns.assignment,
				truncateToWidth(line.Assignment, fontRegular, 8.5, 120),
				fontRegular, 8.5, 0.25)

			note := line.Note
			// A line billed at something other than the base rate has to say
			// why, on the same document as the figure. Prefixed onto the note
			// rather than given a column of its own: it is the exception, and a
			// mostly-empty column would cost every row space to say nothing.
			if line.Kind != "" && line.Kind != string(domain.KindWork) {
				note = "(" + line.Kind + ") " + note
			}
			if line.Status == string(domain.StatusPending) {
				// A pending proposal is listed but counts for nothing, and the
				// document has to say so rather than leaving a reader to wonder
				// why the total does not add up.
				note = "(pending) " + note
			}
			doc.text(pdfColumns.note,
				truncateToWidth(note, fontRegular, 8.5, pdfColumns.hours-pdfColumns.note-70),
				fontRegular, 8.5, 0.35)

			doc.textRight(pdfColumns.hours, domain.FormatDecimalHours(line.Seconds),
				fontRegular, 8.5, 0.15)

			if line.AmountMinor != 0 {
				doc.textRight(pdfColumns.amount,
					domain.NewMoney(line.AmountMinor, line.Currency).Decimal(),
					fontRegular, 8.5, 0.15)
			}
			doc.space(12)
		}

		// A subtotal per group, because a client reading the document wants to
		// know what each project cost without adding it up themselves.
		doc.line(pdfColumns.hours-60, pdfColumns.amount, 0.4, 0.85)
		doc.space(11)
		doc.textRight(pdfColumns.hours, domain.FormatDecimalHours(group.seconds), fontBold, 8.5, 0.15)
		for currency, amount := range group.amounts {
			doc.textRight(pdfColumns.amount,
				domain.NewMoney(amount, currency).Decimal()+" "+currency, fontBold, 8.5, 0.15)
		}
		doc.space(10)
	}

	// ---- totals ------------------------------------------------------------

	if doc.remaining() < 70 {
		doc.newPage()
	}
	doc.space(8)
	doc.line(marginLeft, marginLeft+contentWidth, 0.3, 1.2)
	doc.space(16)

	doc.text(marginLeft, "Total tracked", fontBold, 10, 0)
	doc.textRight(pdfColumns.hours,
		domain.FormatDecimalHours(report.Totals.SummedSeconds), fontBold, 10, 0)
	doc.space(14)

	if report.Totals.BillableSeconds != report.Totals.SummedSeconds {
		doc.text(marginLeft, "Billable", fontRegular, 9.5, 0.25)
		doc.textRight(pdfColumns.hours,
			domain.FormatDecimalHours(report.Totals.BillableSeconds), fontRegular, 9.5, 0.25)
		doc.space(13)
	}

	// Overlap is stated rather than hidden. A client whose invoice shows ten
	// hours across an eight-hour window is owed an explanation, and it is better
	// for the document to give it than for the question to arrive later.
	if report.Totals.OverlapSeconds > 0 {
		doc.text(marginLeft,
			"Includes "+domain.FormatDecimalHours(report.Totals.OverlapSeconds)+
				" h of parallel work (several tasks running at once)",
			fontItalic, 8.5, 0.4)
		doc.space(13)
	}

	currencies := sortedCurrencies(report.Totals.AmountsByCurrency)
	for _, currency := range currencies {
		amount := report.Totals.AmountsByCurrency[currency]
		doc.text(marginLeft, "Total "+currency, fontBold, 10, 0)
		doc.textRight(pdfColumns.amount,
			domain.NewMoney(amount, currency).Decimal()+" "+currency, fontBold, 10, 0)
		doc.space(14)
	}

	// ---- page numbers ------------------------------------------------------

	// Written last, because the total is not known until every page exists.
	for i, page := range doc.pages {
		saved := doc.current
		savedY := doc.y
		doc.current = page
		doc.y = pageHeight - marginBot + 22
		doc.text(marginLeft, report.Title, fontRegular, 7.5, 0.55)
		doc.textRight(marginLeft+contentWidth,
			fmt.Sprintf("Page %d of %d", i+1, len(doc.pages)), fontRegular, 7.5, 0.55)
		doc.current = saved
		doc.y = savedY
	}

	return doc.render(w)
}

// drawPDFTableHead draws the column headings and the rule beneath them.
func drawPDFTableHead(d *pdfDoc, report Report) {
	d.rect(marginLeft-4, contentWidth+8, 14, 0.93)
	d.text(pdfColumns.date, "Date", fontBold, 8, 0.3)
	d.text(pdfColumns.assignment, "Assignment", fontBold, 8, 0.3)
	d.text(pdfColumns.note, "Description", fontBold, 8, 0.3)
	d.textRight(pdfColumns.hours, "Hours", fontBold, 8, 0.3)
	if len(report.Totals.AmountsByCurrency) > 0 {
		d.textRight(pdfColumns.amount, "Amount", fontBold, 8, 0.3)
	}
	d.space(11)
	d.line(marginLeft, marginLeft+contentWidth, 0.6, 0.5)
	d.space(4)
}

// lineGroup is one customer/project block in the document.
type lineGroup struct {
	label   string
	lines   []Line
	seconds int64
	amounts map[string]int64
}

// groupLines arranges entries by customer and project.
//
// A flat chronological list is what the screen shows; a document sent to a
// client wants the work gathered by what it was for. Groups are ordered by
// label so the same report always reads the same way.
func groupLines(lines []Line) []lineGroup {
	byLabel := map[string]*lineGroup{}
	for _, line := range lines {
		label := line.Customer
		if line.Project != "" {
			label += " / " + line.Project
		}
		group, ok := byLabel[label]
		if !ok {
			group = &lineGroup{label: label, amounts: map[string]int64{}}
			byLabel[label] = group
		}
		group.lines = append(group.lines, line)
		// Only entries that count contribute to a subtotal, for the same reason
		// they do not contribute to the report total - and by the same rule, so
		// the subtotals add up to it.
		if line.Counts() {
			group.seconds += line.Seconds
			if line.AmountMinor != 0 && line.Currency != "" {
				group.amounts[line.Currency] += line.AmountMinor
			}
		}
	}

	groups := make([]lineGroup, 0, len(byLabel))
	for _, group := range byLabel {
		sort.Slice(group.lines, func(i, j int) bool {
			return group.lines[i].Start.Before(group.lines[j].Start)
		})
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].label < groups[j].label })
	return groups
}

// sortedCurrencies returns currency codes in a stable order, so a multi-currency
// report does not reorder its totals between runs.
func sortedCurrencies(amounts map[string]int64) []string {
	codes := make([]string, 0, len(amounts))
	for code := range amounts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}
