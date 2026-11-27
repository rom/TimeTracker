package web

import (
	"net/http"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/i18n"
	"github.com/rom/timetracker/internal/service"
)

// The days somebody was not working.

// handleAbsence lists the acting user's absence and the instance's public
// holidays, and offers the form that records one.
func (s *Server) handleAbsence(w http.ResponseWriter, r *http.Request) {
	// A client takes no leave here; their own calendar is not this
	// application's business.
	if s.sentToTheirOwnListing(w, r) {
		return
	}

	data, err := s.newPageData(r, "", "absence")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("absence.title")

	// A year either side, which is what a leave calendar is read over: last
	// year's holiday is a record, next year's is a plan.
	now := s.svc.Now()
	from, to := now.AddDate(-1, 0, 0), now.AddDate(1, 0, 0)

	if data.Absences, err = s.svc.Absences(r.Context(), from, to); err != nil {
		s.fail(w, r, err)
		return
	}
	data.AbsenceKinds = domain.AbsenceKinds
	s.render(w, r, "page_absence.html", data)
}

// handleRecordAbsence stores one.
func (s *Server) handleRecordAbsence(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	// A single day is the common case, so an empty end date means the day the
	// absence starts rather than an error.
	endsOn := r.FormValue("ends_on")
	if endsOn == "" {
		endsOn = r.FormValue("starts_on")
	}

	_, err := s.svc.RecordAbsence(r.Context(), service.AbsenceInput{
		Kind:     domain.AbsenceKind(r.FormValue("kind")),
		StartsOn: r.FormValue("starts_on"),
		EndsOn:   endsOn,
		Note:     r.FormValue("note"),
		UserID:   int64Param(r.FormValue("user_id")),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/absence", http.StatusSeeOther)
}

// handleDeleteAbsence removes one, recoverably.
func (s *Server) handleDeleteAbsence(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	id := int64Param(r.PathValue("id"))
	absence, readErr := s.svc.Absence(r.Context(), id)
	describe := func(p *i18n.Printer) string {
		if readErr != nil {
			return ""
		}
		what := p.T(absence.Kind.MessageKey()) + " · " + absence.StartsOn
		if absence.EndsOn != absence.StartsOn {
			what += "–" + absence.EndsOn
		}
		return what
	}
	if s.needsConfirming(w, r, describe, "/absence") {
		return
	}

	if err := s.svc.DeleteAbsence(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/absence", http.StatusSeeOther)
}
