package web

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Importing a calendar.
//
// Two passes like the CSV import, and for a stronger reason: a calendar is not
// a timesheet. It holds cancelled meetings, meetings the person declined,
// all-day markers for holidays, and blocks somebody put in to protect an
// afternoon. The preview is where those are separated out, by name.

// handleCalendarForm renders the calendar import screen.
func (s *Server) handleCalendarForm(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("calendar.title")
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_calendar.html", data)
}

// handleCalendarPreview parses an uploaded .ics and shows what it would do.
func (s *Server) handleCalendarPreview(w http.ResponseWriter, r *http.Request) {
	body, err := s.readCalendarUpload(w, r)
	if err != nil {
		return
	}

	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("calendar.title")
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}

	preview, err := s.svc.ParseCalendar(r.Context(), bytes.NewReader(body),
		int64Param(r.FormValue("default_assignment")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.CalendarPreview = &preview
	// The file travels back with the form, so committing does not need it
	// uploaded twice. It is the same trade the CSV import declines to make -
	// there, the file can be large; a calendar export is small, and asking
	// somebody to find the file again after reviewing forty meetings is worse.
	data.CalendarFile = string(body)

	s.render(w, r, "page_calendar.html", data)
}

// handleCalendarImport writes the events the user ticked.
func (s *Server) handleCalendarImport(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	// Which events, and what each is going on. The form carries one checkbox
	// and one select per event, keyed by the event's UID.
	selected := map[string]int64{}
	for _, uid := range r.Form["event"] {
		selected[uid] = int64Param(r.FormValue("assignment_" + uid))
	}
	if len(selected) == 0 {
		s.fail(w, r, domainValidation("nothing was selected to import"))
		return
	}

	result, err := s.svc.ImportCalendar(r.Context(),
		strings.NewReader(r.FormValue("calendar")), selected, r.FormValue("note"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("calendar.title")
	data.CalendarResult = &result
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_calendar.html", data)
}

// readCalendarUpload reads the uploaded file, answering the request itself on
// failure.
func (s *Server) readCalendarUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxFormMemory); err != nil {
		http.Error(w, "The uploaded file is too large.", http.StatusRequestEntityTooLarge)
		return nil, err
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file was uploaded.", http.StatusBadRequest)
		return nil, err
	}
	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		s.fail(w, r, err)
		return nil, err
	}
	return body, nil
}
