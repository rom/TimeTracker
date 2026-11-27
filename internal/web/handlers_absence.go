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

	data, err := s.absencePage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_absence.html", data)
}

// absencePage builds what both the listing and the correction form need.
func (s *Server) absencePage(r *http.Request) (pageData, error) {
	data, err := s.newPageData(r, "", "absence")
	if err != nil {
		return data, err
	}
	data.Title = data.Printer.T("absence.title")

	// A year either side, which is what a leave calendar is read over: last
	// year's holiday is a record, next year's is a plan.
	now := s.svc.Now()
	from, to := now.AddDate(-1, 0, 0), now.AddDate(1, 0, 0)

	if data.Absences, err = s.svc.Absences(r.Context(), from, to); err != nil {
		return data, err
	}
	data.AbsenceKinds = domain.AbsenceKinds
	return data, nil
}

// handleEditAbsence shows the leave screen with the form correcting one
// record rather than recording a new one.
//
// The same screen rather than a page of its own: the list is the context for
// the correction - "which of these am I changing" is answered by the row
// highlighted below the form - and a second template would be the same five
// fields twice.
func (s *Server) handleEditAbsence(w http.ResponseWriter, r *http.Request) {
	if s.sentToTheirOwnListing(w, r) {
		return
	}

	// Loaded through the service, so somebody who may not see this record gets
	// the same answer as if it did not exist.
	absence, err := s.svc.Absence(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	data, err := s.absencePage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.EditingAbsence = &absence
	s.render(w, r, "page_absence.html", data)
}

// handleUpdateAbsence saves a correction.
func (s *Server) handleUpdateAbsence(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	if _, err := s.svc.UpdateAbsence(r.Context(), int64Param(r.PathValue("id")),
		s.absenceInputFromForm(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/absence", http.StatusSeeOther)
}

// absenceInputFromForm reads the five fields both forms share.
func (s *Server) absenceInputFromForm(r *http.Request) service.AbsenceInput {
	// A single day is the common case, so an empty end date means the day the
	// absence starts rather than an error.
	endsOn := r.FormValue("ends_on")
	if endsOn == "" {
		endsOn = r.FormValue("starts_on")
	}
	return service.AbsenceInput{
		Kind:     domain.AbsenceKind(r.FormValue("kind")),
		StartsOn: r.FormValue("starts_on"),
		EndsOn:   endsOn,
		Note:     r.FormValue("note"),
		UserID:   int64Param(r.FormValue("user_id")),
	}
}

// handleRecordAbsence stores one.
func (s *Server) handleRecordAbsence(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	_, err := s.svc.RecordAbsence(r.Context(), s.absenceInputFromForm(r))
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
