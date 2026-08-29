package web

import (
	"net/http"

	"github.com/rom/timetracker/internal/domain"
)

// handleDismissReminder records that somebody does not want to be told again
// about one nudge, for one day or one week.
//
// The only write reminders have. Everything else about them is computed when a
// screen renders (docs/adr/0034-reminders-are-shown-not-sent.md).
func (s *Server) handleDismissReminder(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	kind := domain.ReminderKind(r.PathValue("kind"))
	if err := s.svc.DismissReminder(r.Context(), kind, r.FormValue("scope")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}
