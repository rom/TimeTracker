package web

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/export"
)

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

	entries, err := s.svc.Entries(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	user, _ := auth.UserFrom(r.Context())
	report := export.NewReport(
		fmt.Sprintf("Time report %s to %s", form.From, form.To),
		filter.From, filter.To, user.TimeZone, user.DisplayName,
		entries, s.svc.Now())

	filename := fmt.Sprintf("timetracker-%s-%s.%s", form.From, form.To, format)

	// Content-Disposition: attachment, so a browser saves the file rather than
	// trying to display it.
	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if err := format.Write(w, report); err != nil {
		// The status line is already sent by this point, so the download will be
		// truncated. Logging is all that remains; the alternative is buffering an
		// unbounded report in memory to be able to fail cleanly.
		s.log.ErrorContext(r.Context(), "export failed midway",
			"format", string(format), "error", err.Error())
	}
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
