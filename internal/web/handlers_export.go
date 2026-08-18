package web

import (
	"fmt"
	"net/http"

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

	switch format {
	case export.FormatCSV:
		// Content-Disposition: attachment, so a browser saves the file rather
		// than trying to display it.
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", contentDisposition(filename))
		if err := export.WriteCSV(w, report); err != nil {
			// The status line is already sent by this point, so the download will
			// be truncated. Logging is all that remains; the alternative is
			// buffering an unbounded report in memory to be able to fail cleanly.
			s.log.ErrorContext(r.Context(), "CSV export failed midway",
				"error", err.Error())
		}

	case export.FormatJSON:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", contentDisposition(filename))
		if err := export.WriteJSON(w, report); err != nil {
			s.log.ErrorContext(r.Context(), "JSON export failed midway",
				"error", err.Error())
		}

	case export.FormatPDF, export.FormatDOCX:
		// Planned for layer 5; see docs/MVP_PLAN.md. Answering honestly is better
		// than a broken download.
		http.Error(w,
			"PDF and DOCX export are not implemented yet. CSV and JSON are available.",
			http.StatusNotImplemented)

	default:
		http.Error(w, "Unknown export format.", http.StatusBadRequest)
	}
}

// contentDisposition builds the header that names a downloaded file.
//
// The filename is composed here from validated dates and a known format, never
// from user text, so there is nothing to escape or inject.
func contentDisposition(filename string) string {
	return fmt.Sprintf(`attachment; filename="%s"`, filename)
}
