package web

import (
	"net/http"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Idle observations over HTTP.
//
// Two routes, and the asymmetry between them is the point. Recording is done by
// the page and says only what it saw; resolving is done by a person and is the
// only thing that changes a timesheet (ADR-0033).

// handleRecordIdle takes the page's report of a stretch it saw nothing during.
//
// Answered with 204 whether or not anything was stored. A stretch the server
// declines - too short, outside the entry, already covered by one it has, or
// arriving when idle observation is switched off - is not the page's mistake,
// and a browser that gets an error for it would have nothing useful to do about
// it. What must not happen is a silent 200 for a malformed request, so the
// fields themselves are validated.
func (s *Server) handleRecordIdle(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	entryID := int64Param(r.FormValue("entry_id"))
	if entryID == 0 {
		http.Error(w, "An observation needs the entry it is about.", http.StatusBadRequest)
		return
	}
	from, fromErr := time.Parse(time.RFC3339, r.FormValue("from"))
	to, toErr := time.Parse(time.RFC3339, r.FormValue("to"))
	if fromErr != nil || toErr != nil {
		http.Error(w, "An observation needs a start and an end, in RFC 3339.",
			http.StatusBadRequest)
		return
	}
	source := domain.IdleSource(r.FormValue("source"))
	if !domain.KnownIdleSource(source) {
		http.Error(w, "Unknown observation source.", http.StatusBadRequest)
		return
	}

	if _, err := s.svc.RecordIdle(r.Context(), entryID, from, to, source); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResolveIdle applies the decision somebody made about a stretch.
func (s *Server) handleResolveIdle(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	id := int64Param(r.PathValue("id"))
	resolution := domain.IdleResolution(r.FormValue("resolution"))
	if !domain.KnownIdleResolution(resolution) {
		http.Error(w, "Unknown decision.", http.StatusBadRequest)
		return
	}

	if err := s.svc.ResolveIdle(r.Context(), id, resolution); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}
