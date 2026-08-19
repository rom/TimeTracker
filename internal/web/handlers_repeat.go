package web

import (
	"net/http"
	"strconv"

	"github.com/rom/timetracker/internal/domain"
)

// Copying a day or a week, routines, and switching timers.

// handleCopyDay duplicates one day onto another.
func (s *Server) handleCopyDay(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	loc := userLocation(r)
	target := s.dateParam(r, "date")
	// Where from. Defaults to the previous day, which is what "copy yesterday"
	// means and is the overwhelmingly common case.
	source := target.AddDate(0, 0, -1)
	if raw := r.FormValue("from"); raw != "" {
		if parsed, err := timeParseIn(raw, loc); err == nil {
			source = parsed
		}
	}

	if _, err := s.svc.CopyDay(r.Context(), source, target); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleCopyWeek duplicates a week onto another, day for day.
func (s *Server) handleCopyWeek(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	loc := userLocation(r)
	target := s.dateParam(r, "date")
	source := target.AddDate(0, 0, -7)
	if raw := r.FormValue("from"); raw != "" {
		if parsed, err := timeParseIn(raw, loc); err == nil {
			source = parsed
		}
	}

	if _, err := s.svc.CopyWeek(r.Context(), source, target); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleSwitchTimer stops what is running and starts something else.
func (s *Server) handleSwitchTimer(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	_, err := s.svc.SwitchTo(r.Context(),
		int64Param(r.FormValue("assignment_id")), r.FormValue("note"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleApplyRoutine records one routine's entry on a day.
func (s *Server) handleApplyRoutine(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	_, err := s.svc.ApplyRoutine(r.Context(),
		int64Param(r.PathValue("id")), s.dateParam(r, "date"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleApplyAllRoutines records every routine due on a day.
func (s *Server) handleApplyAllRoutines(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.svc.ApplyAllRoutines(r.Context(), s.dateParam(r, "date")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleRoutines renders the routine management screen.
func (s *Server) handleRoutines(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "routines")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("routines.title")

	if data.Routines, err = s.svc.Routines(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if editID := int64Param(r.URL.Query().Get("edit")); editID != 0 {
		for i := range data.Routines {
			if data.Routines[i].ID == editID {
				data.EditRoutine = &data.Routines[i]
				break
			}
		}
	}
	s.render(w, r, "page_routines.html", data)
}

// handleSaveRoutine creates or updates a template.
func (s *Server) handleSaveRoutine(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	duration, err := domain.ParseDuration(r.FormValue("duration"))
	if err != nil {
		s.fail(w, r, domainValidation(
			"could not read the length "+strconv.Quote(r.FormValue("duration"))+
				": try 30m, 1h or 1h30"))
		return
	}

	// The weekdays arrive as repeated checkbox values.
	var weekdays []int
	for _, raw := range r.Form["weekdays"] {
		if day, convErr := strconv.Atoi(raw); convErr == nil {
			weekdays = append(weekdays, day)
		}
	}

	kind := domain.EntryKind(r.FormValue("kind"))
	if kind != "" && !kind.Valid() {
		s.fail(w, r, domainValidation("unknown kind of time: "+r.FormValue("kind")))
		return
	}

	routine := domain.Routine{
		ID:              int64Param(r.FormValue("routine_id")),
		AssignmentID:    int64Param(r.FormValue("assignment_id")),
		Name:            r.FormValue("name"),
		Note:            r.FormValue("note"),
		Weekdays:        weekdays,
		StartTime:       r.FormValue("start"),
		DurationSeconds: duration,
		Billable:        r.FormValue("billable") != "",
		Kind:            kind,
		Tags:            domain.ParseTagList(r.FormValue("tags")),
		Active:          r.FormValue("active") != "",
		SortOrder:       int(int64Param(r.FormValue("sort_order"))),
	}
	if err := s.svc.SaveRoutine(r.Context(), routine); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/routines", http.StatusSeeOther)
}

// handleDeleteRoutine removes a template.
func (s *Server) handleDeleteRoutine(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteRoutine(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/routines", http.StatusSeeOther)
}
