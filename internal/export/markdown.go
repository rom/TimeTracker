package export

import (
	"fmt"
	"io"
	"strings"

	"github.com/rom/timetracker/internal/domain"
)

// WriteMarkdown renders a report as CommonMark.
//
// Markdown exists here for the case the other four formats do not cover: pasting
// a week into a ticket, a wiki, a pull request or an email. CSV is for a
// spreadsheet, JSON for a machine, PDF and DOCX for a client - and all four are
// useless when what somebody needs is text they can paste into a comment box and
// have come out readable.
//
// It is written to be legible *as source* as well as rendered, because half the
// places it gets pasted will never render it. That is why the tables are padded
// to an even width: a Markdown table nobody renders should still line up.

func WriteMarkdown(w io.Writer, report Report) error {
	var out strings.Builder

	out.WriteString("# " + escapeMarkdown(report.Title) + "\n\n")

	// The metadata block, as a definition-ish list. Kept to the facts a reader
	// needs to reproduce the figures: which period, whose, in what zone, and
	// when it was taken.
	period := fmt.Sprintf("%s – %s",
		report.From.Format("2006-01-02"),
		report.To.AddDate(0, 0, -1).Format("2006-01-02"))
	out.WriteString("**Period:** " + period + "  \n")
	if report.User != "" {
		out.WriteString("**For:** " + escapeMarkdown(report.User) + "  \n")
	}
	if report.TimeZone != "" {
		out.WriteString("**Time zone:** " + escapeMarkdown(report.TimeZone) + "  \n")
	}
	out.WriteString("**Generated:** " + report.GeneratedAt.Format("2006-01-02 15:04") + " UTC\n\n")

	withAmounts := len(report.Totals.AmountsByCurrency) > 0

	for _, group := range groupLines(report.Lines) {
		out.WriteString("## " + escapeMarkdown(group.label) + "\n\n")

		header := []string{"Date", "Assignment", "Description", "Hours"}
		align := []string{"left", "left", "left", "right"}
		if withAmounts {
			header = append(header, "Amount")
			align = append(align, "right")
		}

		rows := make([][]string, 0, len(group.lines)+1)
		for _, line := range group.lines {
			row := []string{
				line.Date.Format("2006-01-02"),
				escapeMarkdown(line.Assignment),
				escapeMarkdown(annotateNote(line)),
				domain.FormatDecimalHours(line.Seconds),
			}
			if withAmounts {
				amount := ""
				if line.AmountMinor != 0 {
					amount = domain.NewMoney(line.AmountMinor, line.Currency).Decimal() +
						" " + line.Currency
				}
				row = append(row, amount)
			}
			rows = append(rows, row)
		}

		// The subtotal as a final row rather than a paragraph underneath: it
		// belongs to the table's arithmetic, and a reader scanning the right
		// edge should find it where the other figures are.
		subtotal := []string{"", "**Subtotal**", "", "**" + domain.FormatDecimalHours(group.seconds) + "**"}
		if withAmounts {
			var parts []string
			for _, currency := range sortedCurrencies(group.amounts) {
				parts = append(parts,
					domain.NewMoney(group.amounts[currency], currency).Decimal()+" "+currency)
			}
			subtotal = append(subtotal, boldIfSet(strings.Join(parts, ", ")))
		}
		rows = append(rows, subtotal)

		writeMarkdownTable(&out, header, align, rows)
		out.WriteString("\n")
	}

	// ---- totals -------------------------------------------------------------

	out.WriteString("## Totals\n\n")
	out.WriteString(fmt.Sprintf("- **Tracked:** %s h\n",
		domain.FormatDecimalHours(report.Totals.SummedSeconds)))
	if report.Totals.BillableSeconds != report.Totals.SummedSeconds {
		out.WriteString(fmt.Sprintf("- **Billable:** %s h\n",
			domain.FormatDecimalHours(report.Totals.BillableSeconds)))
	}
	// Stated rather than hidden, exactly as in the PDF: an invoice showing ten
	// hours inside an eight-hour window is owed an explanation, and the document
	// should give it before the question arrives.
	if report.Totals.OverlapSeconds > 0 {
		out.WriteString(fmt.Sprintf(
			"- **Parallel work:** %s h of the above was recorded on several tasks at once\n",
			domain.FormatDecimalHours(report.Totals.OverlapSeconds)))
	}
	for _, currency := range sortedCurrencies(report.Totals.AmountsByCurrency) {
		out.WriteString(fmt.Sprintf("- **Total %s:** %s\n", currency,
			domain.NewMoney(report.Totals.AmountsByCurrency[currency], currency).Decimal()))
	}
	out.WriteString(fmt.Sprintf("- **Entries:** %d\n", report.Totals.EntryCount))

	_, err := io.WriteString(w, out.String())
	return err
}

// annotateNote prefixes a line's note with anything that explains its figure.
//
// The same reasoning as the PDF's: a line billed at one and a half times the
// base rate, or one that counts for nothing because it is still a proposal, has
// to say so on the document the figure appears on.
func annotateNote(line Line) string {
	note := line.Note
	if line.Kind != "" && line.Kind != string(domain.KindWork) {
		note = "(" + line.Kind + ") " + note
	}
	if line.Status == string(domain.StatusPending) {
		note = "(pending) " + note
	}
	if len(line.Tags) > 0 {
		// Tags come last and keep their hash, so they read the way they are
		// typed into the search box.
		note = strings.TrimSpace(note + " #" + strings.Join(line.Tags, " #"))
	}
	return strings.TrimSpace(note)
}

// writeMarkdownTable emits a pipe table with padded columns.
//
// The padding is what makes the output readable where it is never rendered - a
// commit message, a plain-text mail, a terminal. It costs a few bytes and buys
// the format its main advantage over CSV.
func writeMarkdownTable(out *strings.Builder, header []string, align []string, rows [][]string) {
	widths := make([]int, len(header))
	for i, cell := range header {
		widths[i] = displayWidth(cell)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && displayWidth(cell) > widths[i] {
				widths[i] = displayWidth(cell)
			}
		}
	}
	// Three is the narrowest a delimiter row can be and still carry an
	// alignment colon at each end.
	for i := range widths {
		if widths[i] < 3 {
			widths[i] = 3
		}
	}

	writeRow := func(cells []string) {
		out.WriteString("|")
		for i := range header {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			pad := strings.Repeat(" ", widths[i]-displayWidth(cell))
			if align[i] == "right" {
				out.WriteString(" " + pad + cell + " |")
			} else {
				out.WriteString(" " + cell + pad + " |")
			}
		}
		out.WriteString("\n")
	}

	writeRow(header)

	out.WriteString("|")
	for i := range header {
		if align[i] == "right" {
			out.WriteString(" " + strings.Repeat("-", widths[i]-1) + ": |")
		} else {
			out.WriteString(" " + strings.Repeat("-", widths[i]) + " |")
		}
	}
	out.WriteString("\n")

	for _, row := range rows {
		writeRow(row)
	}
}

// displayWidth counts runes rather than bytes.
//
// A customer called "Björk" is five columns wide and six bytes long, and padding
// by bytes is what makes an exported table look ragged to exactly the people
// whose names are not ASCII.
func displayWidth(s string) int { return len([]rune(s)) }

// escapeMarkdown neutralises the characters that would otherwise turn a
// customer's name into formatting.
//
// A pipe inside a table cell ends the cell, and a note beginning with "#" or "-"
// becomes a heading or a list item. Neither is a security problem - the output
// is a file the user downloads, not a page this application renders - but a
// mangled table is still a wrong document.
func escapeMarkdown(s string) string {
	s = strings.NewReplacer(
		`\`, `\\`,
		"|", `\|`,
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"`", "\\`",
		// Newlines would break out of the table row entirely. A note is one
		// cell, however it was typed.
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
	).Replace(s)
	// Leading markers only matter at the start of a cell, and escaping every
	// hash would spoil the tags appended to a note.
	for _, marker := range []string{"#", "-", "+", ">"} {
		if strings.HasPrefix(s, marker) {
			s = `\` + s
			break
		}
	}
	return s
}

// boldIfSet emboldens a subtotal, leaving an empty cell empty rather than
// rendering a pair of asterisks with nothing between them.
func boldIfSet(s string) string {
	if s == "" {
		return ""
	}
	return "**" + s + "**"
}
