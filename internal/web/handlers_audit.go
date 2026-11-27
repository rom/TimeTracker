package web

import (
	"net/http"

	"github.com/rom/timetracker/internal/service"
)

// Reading the audit trail.
//
// Two surfaces for two questions. "What happened here" is this screen, and is
// administration. "Who changed this entry" is the panel on the entry itself,
// which is bounded by whoever may read that entry and lives in the handler for
// it.

// handleAudit shows the instance's history, newest first.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.sentToTheirOwnListing(w, r) {
		return
	}

	data, err := s.newPageData(r, "", "audit")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("audit.title")

	page, err := s.svc.AuditLog(r.Context(), service.AuditQuery{
		ActorID:      int64Param(r.URL.Query().Get("actor")),
		Action:       r.URL.Query().Get("action"),
		ResourceType: r.URL.Query().Get("resource"),
		From:         r.URL.Query().Get("from"),
		To:           r.URL.Query().Get("to"),
		Page:         int(int64Param(r.URL.Query().Get("page"))),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Audit = &page
	s.render(w, r, "page_audit.html", data)
}
