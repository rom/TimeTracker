package web

import (
	"net/http"

	"github.com/rom/timetracker/internal/i18n"
	"github.com/rom/timetracker/internal/service"
)

// handleBulkEntries applies one change to the selected entries.
func (s *Server) handleBulkEntries(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	// The checkboxes all carry the same name, which is what makes a selection a
	// list rather than forty fields.
	var ids []int64
	for _, raw := range r.Form["id"] {
		if id := int64Param(raw); id != 0 {
			ids = append(ids, id)
		}
	}

	action := service.BulkAction(r.FormValue("action"))
	value := r.FormValue("value")

	// Deleting many at once is asked about like deleting one, and says how
	// many: "delete 23 entries?" is a different question from "delete this
	// entry?", and the number is the part worth reading.
	if action == service.BulkDelete {
		describe := func(p *i18n.Printer) string {
			if len(ids) == 0 {
				return ""
			}
			return p.N("bulk.confirm.entries", len(ids))
		}
		if s.needsConfirming(w, r, describe, "/entries") {
			return
		}
	}

	result, err := s.svc.BulkEntries(r.Context(), ids, action, value)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Shown on a page of its own rather than flashed over the listing. A bulk
	// change can refuse some of what it was given - a week since approved, an
	// entry belonging to somebody else - and the reasons are the useful part;
	// a banner that scrolls away, or a redirect that drops them into a query
	// string, loses exactly the half somebody needs to act on. The import
	// screens report themselves the same way, for the same reason.
	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("bulk.result.title")
	data.Bulk = result
	s.render(w, r, "page_bulk_result.html", data)
}
