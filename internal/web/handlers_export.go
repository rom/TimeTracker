package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/export"
	"github.com/rom/timetracker/internal/preview"
	"github.com/rom/timetracker/internal/service"
)

// handleExportPreview says what a download of the current selection will
// contain, before it is downloaded.
//
// An export is the basis of an invoice, and the screen it is taken from shows
// fifty rows at a time while the download covers the whole filter - so the two
// never looked alike, and the figure that reaches an invoice was first seen in
// a spreadsheet, where it is too late for "that is not the period I meant".
//
// It reads the same filter the export reads, so it cannot describe a different
// document from the one that arrives.
func (s *Server) handleExportPreview(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("export.preview.title")

	filter, form := s.entryFilter(r)
	data.Filter = form

	preview, err := s.svc.PreviewExport(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.ExportPreview = &preview
	data.Formats = export.Formats

	s.render(w, r, "page_export_preview.html", data)
}

// handleExportView serves an export document for reading rather than for
// saving.
//
// The screen before this one says what a download will contain - the rows, the
// totals, the amounts per customer. That answers "is this the right period",
// and it does not answer "is this the right document": the PDF has a layout, a
// title and a set of subtotals, and a wrong one of those is the kind of thing
// somebody notices only once it has been sent to a client.
//
// So this renders the same document the download writes and serves it inline,
// with the sandboxed headers the attachment previews use. A DOCX is a zip,
// which no browser displays, so it is handed to the same extractor that
// previews an uploaded one and shown as the text it holds. The other formats
// are text or PDF and the browser shows them itself.
func (s *Server) handleExportView(w http.ResponseWriter, r *http.Request) {
	format := export.Format(r.PathValue("format"))
	if !format.Known() {
		http.Error(w, "Unknown export format.", http.StatusBadRequest)
		return
	}
	filter, form := s.entryFilter(r)
	// As the download: the filter, never the page.
	filter.Limit, filter.Offset = 0, 0

	count, err := s.svc.CountEntries(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Bounded for every format, not only the ones that cannot stream: this
	// writes into memory so that it can still refuse cleanly, and a document
	// being read on a screen is far smaller than one being archived anyway.
	if count > maxViewedExportRows {
		http.Error(w, s.tr(r).T("export.view.toolarge", count, maxViewedExportRows),
			http.StatusRequestEntityTooLarge)
		return
	}

	entries, err := s.svc.Entries(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	user, _ := auth.UserFrom(r.Context())
	meta := s.exportMeta(form, filter, user)
	report := export.NewReport(meta.Title, meta.From, meta.To,
		meta.TimeZone, meta.User, entries, s.svc.Now())

	// Into a buffer, so a failure half way through is an error page rather than
	// half a document under a 200.
	var document bytes.Buffer
	if err := format.Write(&document, report); err != nil {
		s.fail(w, r, err)
		return
	}

	contentType, body := format.ContentType(), document.Bytes()
	filename := exportFilename(form, format)
	if format == export.FormatDOCX {
		result, err := preview.Render("application/zip", filename, bytes.NewReader(body))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		contentType, body = "text/plain; charset=utf-8", []byte(result.Text)
		// Named for what it now is, as a converted attachment preview is: a
		// browser told to save this would otherwise write a .docx holding
		// plain text, which no word processor opens.
		filename = strings.TrimSuffix(filename, ".docx") + ".txt"
	} else if format != export.FormatPDF {
		// CSV, JSON and Markdown are text, and a browser handed text/csv saves
		// it instead of showing it - which is the behaviour this route exists
		// to avoid.
		contentType = "text/plain; charset=utf-8"
	}

	// The same headers as an attachment preview: nothing loads, nothing runs,
	// and the document is displayed rather than saved.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	// Private and uncached: it is a view of live data, and it is somebody's
	// billing.
	w.Header().Set("Cache-Control", "no-store, private")

	if _, err := w.Write(body); err != nil {
		s.log.ErrorContext(r.Context(), "export view failed midway",
			"format", string(format), "error", err.Error())
	}
}

// maxViewedExportRows bounds a document rendered for reading.
//
// Lower than the download's bound on purpose: this one is held in memory in
// order to fail cleanly, and nobody reads five thousand rows on a screen. The
// message says to download instead, which is the route that streams.
//
// A var rather than a const so a test can lower it, as the download's bound is:
// the real figure is far above anything a test should seed, and a bound nothing
// reaches is a bound nothing checks.
var maxViewedExportRows = 5000

// exportMeta is the heading a report carries, built once so the download and
// the inline view cannot disagree about the period they cover.
func (s *Server) exportMeta(form entryFilterForm, filter service.EntryFilter,
	user domain.User) export.Meta {
	return export.Meta{
		Title:       fmt.Sprintf("Time report %s to %s", form.From, form.To),
		GeneratedAt: s.svc.Now().UTC(),
		From:        filter.From,
		To:          filter.To,
		TimeZone:    user.TimeZone,
		User:        user.DisplayName,
	}
}

// handleExport renders the current entry selection in the requested format.
//
// The same filter drives the Entries screen and the export, so what a user
// downloads is exactly what they were looking at - which matters when the
// download becomes the basis of an invoice.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := export.Format(r.PathValue("format"))
	if !format.Known() {
		http.Error(w, "Unknown export format.", http.StatusBadRequest)
		return
	}
	filter, form := s.entryFilter(r)

	// An export covers the filter, never the page. The screen shows fifty rows
	// at a time and the download shows all of them - and until this line existed
	// it did neither: the screen's row cap travelled into the export with the
	// rest of the filter, so any range with more entries than the cap was
	// silently truncated, oldest first, in a file somebody was about to invoice
	// from. A truncated export is worse than a slow one, which is the same
	// reasoning the backup writer states.
	filter.Limit, filter.Offset = 0, 0

	user, _ := auth.UserFrom(r.Context())
	meta := s.exportMeta(form, filter, user)

	// The document formats group by customer and project, which means the last
	// row read can belong to the first block written - so they have to hold the
	// whole report. Refused above a stated size rather than attempted: a PDF of
	// two hundred thousand lines is not a document anybody wants, and running
	// out of memory is a worse way to find that out than being told.
	if !format.Streams() || !filter.Streamable() {
		s.writeCollectedExport(w, r, format, filter, meta, form)
		return
	}

	// The first row is pulled before a single header is set, so a query that
	// fails - a malformed regular expression is the everyday case - is answered
	// with the message that says how to fix it rather than with a one-line CSV
	// carrying a 200.
	lines, err := export.Primed(export.LinesOf(s.svc.EachEntry(r.Context(), filter), s.svc.Now()))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	filename := exportFilename(form, format)
	// Content-Disposition: attachment, so a browser saves the file rather than
	// trying to display it.
	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	stream := export.Stream{Meta: meta, Now: s.svc.Now(), Lines: lines}
	if err := format.WriteStream(w, stream); err != nil {
		// The status line went out with the first row, so the download is
		// already truncated. Logging is all that remains - and it is the price
		// of streaming, paid deliberately: the alternative is holding the whole
		// report in memory to be able to fail cleanly, which is the thing this
		// path exists to avoid.
		s.log.ErrorContext(r.Context(), "streamed export failed midway",
			"format", string(format), "error", err.Error())
	}
}

// writeCollectedExport renders a format that cannot be streamed.
//
// PDF, DOCX and Markdown group their rows and print a subtotal per group, so
// none of them can write anything until every row has been read. They are
// bounded instead, and the bound is reported as a refusal with a way forward
// rather than as whatever the machine does when it runs out of memory.
func (s *Server) writeCollectedExport(w http.ResponseWriter, r *http.Request,
	format export.Format, filter service.EntryFilter, meta export.Meta, form entryFilterForm) {

	count, err := s.svc.CountEntries(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if count > maxCollectedExportRows {
		http.Error(w, s.tr(r).T("export.toolarge", count, maxCollectedExportRows), http.StatusRequestEntityTooLarge)
		return
	}

	entries, err := s.svc.Entries(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	report := export.NewReport(meta.Title, meta.From, meta.To,
		meta.TimeZone, meta.User, entries, s.svc.Now())

	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Disposition", contentDisposition(exportFilename(form, format)))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if err := format.Write(w, report); err != nil {
		s.log.ErrorContext(r.Context(), "export failed midway",
			"format", string(format), "error", err.Error())
	}
}

// maxCollectedExportRows bounds the formats that cannot stream.
//
// Fifty thousand lines is already far past a document anybody reads; it is here
// so that asking for a decade of a busy team in PDF fails with a sentence rather
// than by exhausting the machine. CSV and JSON have no such limit, and the
// message says so.
var maxCollectedExportRows = 50_000

// exportFilename names the download.
func exportFilename(form entryFilterForm, format export.Format) string {
	return fmt.Sprintf("timetracker-%s-%s.%s", form.From, form.To, format)
}

// PreviewURL is the link to the preview of this filter's export, carrying the
// whole filter so the preview describes exactly what the download would hold.
func (f entryFilterForm) PreviewURL() template.URL {
	return template.URL("/export/preview?" + f.exportQuery())
}

// ExportURL is the download link for one format, carrying the whole filter.
//
// It exists because the alternative - repeating the parameters in the template
// once per format - is how "customer" ended up on the download link and "tags",
// "kind" and the search query did not. An export that quietly covers more than
// the screen it was taken from is the one bug this feature must not have, so the
// URL is built once, from the same struct the screen was rendered with.
//
// It returns template.URL because html/template would otherwise percent-escape
// the separators in the query string and produce one parameter with an
// ampersand in its name. That is safe here and only here: every character in the
// result was either written above as a literal, produced by url.Values.Encode,
// or is a format from export.Formats, which is a fixed list of constants. No
// user text reaches this unescaped.
func (f entryFilterForm) ExportURL(format export.Format) template.URL {
	return template.URL("/export/" + string(format) + "?" + f.exportQuery())
}

// ViewURL is the link that shows one format's document in the browser instead
// of saving it, carrying the whole filter for the same reason ExportURL does.
func (f entryFilterForm) ViewURL(format export.Format) template.URL {
	return template.URL("/export/" + string(format) + "/view?" + f.exportQuery())
}

// PageURL is the link to another page of the same filter.
//
// Built from the same query as the export links, plus the page. That sharing is
// the point: a pager that dropped the search box would send somebody who
// clicked "next" to page two of a different result.
func (f entryFilterForm) PageURL(page int) template.URL {
	query := f.exportQuery()
	if page > 1 {
		query += "&page=" + strconv.Itoa(page)
	}
	return template.URL("/entries?" + query)
}

// Pages is how many pages the filter's results fill.
func (f entryFilterForm) Pages() int {
	if f.Total <= 0 {
		return 1
	}
	return (f.Total + EntriesPerPage - 1) / EntriesPerPage
}

// HasPages reports whether there is more than one, so a pager is worth drawing.
func (f entryFilterForm) HasPages() bool { return f.Pages() > 1 }

// FirstShown and LastShown bound the page in the result, for "51-100 of 237".
//
// A count somebody can check against the rows in front of them: a pager that
// says only "page 2" leaves them to work out what they are looking at.
func (f entryFilterForm) FirstShown() int {
	if f.Total == 0 {
		return 0
	}
	return (f.Page-1)*EntriesPerPage + 1
}

func (f entryFilterForm) LastShown() int {
	last := f.Page * EntriesPerPage
	if last > f.Total {
		last = f.Total
	}
	return last
}

// PageNumbers is the window of page links to draw.
//
// The first and last page always, the current one and its neighbours, and a gap
// where pages were left out - which is what stops a filter matching ten thousand
// entries from rendering two hundred links. A zero marks the gap.
func (f entryFilterForm) PageNumbers() []int {
	pages := f.Pages()
	const window = 2

	wanted := map[int]bool{1: true, pages: true}
	for page := f.Page - window; page <= f.Page+window; page++ {
		if page >= 1 && page <= pages {
			wanted[page] = true
		}
	}

	numbers := make([]int, 0, len(wanted)+2)
	previous := 0
	for page := 1; page <= pages; page++ {
		if !wanted[page] {
			continue
		}
		if previous != 0 && page != previous+1 {
			numbers = append(numbers, 0)
		}
		numbers = append(numbers, page)
		previous = page
	}
	return numbers
}

// Narrowed reports whether the filter is anything other than the default view,
// so the screen can offer to clear it only when there is something to clear.
//
// The date range is excluded on purpose: it always has a value, so counting it
// would mean the button never went away.
func (f entryFilterForm) Narrowed() bool {
	return f.CustomerID != 0 || f.ProjectID != 0 || f.AssignmentID != 0 ||
		f.BillableOnly || f.WithExpenses || f.Tags != "" || f.Kind != "" || f.Query != ""
}

// exportQuery renders the current filter as a query string.
func (f entryFilterForm) exportQuery() string {
	values := url.Values{}
	values.Set("from", f.From)
	values.Set("to", f.To)
	if f.CustomerID != 0 {
		values.Set("customer", fmt.Sprint(f.CustomerID))
	}
	if f.ProjectID != 0 {
		values.Set("project", fmt.Sprint(f.ProjectID))
	}
	if f.AssignmentID != 0 {
		values.Set("assignment", fmt.Sprint(f.AssignmentID))
	}
	if f.BillableOnly {
		values.Set("billable", "1")
	}
	if f.WithExpenses {
		values.Set("expenses", "1")
	}
	if f.Tags != "" {
		values.Set("tags", f.Tags)
	}
	if f.Kind != "" {
		values.Set("kind", f.Kind)
	}
	if f.Query != "" {
		values.Set("q", f.Query)
	}
	if f.UseRegexp {
		values.Set("regexp", "1")
	}
	return values.Encode()
}

// contentDisposition builds the header that names a downloaded file.
//
// The filename is composed here from validated dates and a known format, never
// from user text, so there is nothing to escape or inject.
func contentDisposition(filename string) string {
	return fmt.Sprintf(`attachment; filename="%s"`, filename)
}
