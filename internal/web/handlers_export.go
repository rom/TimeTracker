package web

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"

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
