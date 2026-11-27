package web

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/rom/timetracker/internal/domain"
)

// Importing a calendar.
//
// Two passes like the CSV import, and for a stronger reason: a calendar is not
// a timesheet. It holds cancelled meetings, meetings the person declined,
// all-day markers for holidays, and blocks somebody put in to protect an
// afternoon. The preview is where those are separated out, by name.

// handleCalendarForm renders the calendar import screen.
func (s *Server) handleCalendarForm(w http.ResponseWriter, r *http.Request) {
	if s.sentToTheirOwnListing(w, r) {
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
	data.AbsenceKinds = domain.AbsenceKinds
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

	data.AbsenceKinds = domain.AbsenceKinds
	data.CalendarTarget = r.FormValue("target")
	data.CalendarKind = r.FormValue("absence_kind")

	// The file travels back with the form, so committing does not need it
	// uploaded twice. It is the same trade the CSV import declines to make -
	// there, the file can be large; a calendar export is small, and asking
	// somebody to find the file again after reviewing forty meetings is worse.
	data.CalendarFile = string(body)

	// The same file, read two ways. A calendar read as a timesheet and a
	// calendar read as a leave register disagree about every event in it, so
	// they are two previews rather than one with a column that changes.
	if data.CalendarTarget == calendarAsAbsence {
		preview, err := s.svc.ParseCalendarAsAbsence(r.Context(),
			bytes.NewReader(body), domain.AbsenceKind(data.CalendarKind))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data.HolidayPreview = &preview
		s.render(w, r, "page_calendar.html", data)
		return
	}

	preview, err := s.svc.ParseCalendar(r.Context(), bytes.NewReader(body),
		int64Param(r.FormValue("default_assignment")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.CalendarPreview = &preview

	s.render(w, r, "page_calendar.html", data)
}

// calendarAsAbsence is the target that reads a file as days away.
const calendarAsAbsence = "absence"

// handleCalendarImport writes the events the user ticked.
func (s *Server) handleCalendarImport(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	if r.FormValue("target") == calendarAsAbsence {
		s.importCalendarAsAbsence(w, r)
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

// importCalendarAsAbsence records the all-day events the user ticked.
//
// No assignment to choose here, so the form carries checkboxes alone and the
// kind is one choice for the whole file.
func (s *Server) importCalendarAsAbsence(w http.ResponseWriter, r *http.Request) {
	selected := map[string]bool{}
	for _, uid := range r.Form["event"] {
		selected[uid] = true
	}
	if len(selected) == 0 {
		s.fail(w, r, domainValidation("nothing was selected to import"))
		return
	}

	result, err := s.svc.ImportCalendarAsAbsence(r.Context(),
		strings.NewReader(r.FormValue("calendar")),
		domain.AbsenceKind(r.FormValue("absence_kind")), selected)
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
	data.AbsenceKinds = domain.AbsenceKinds
	data.HolidayResult = &result
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
