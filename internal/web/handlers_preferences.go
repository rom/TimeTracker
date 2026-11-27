package web

import (
	"net/http"
	"strconv"

	"github.com/rom/timetracker/internal/service"
)

// The personal half of the settings.
//
// Theme and language have their own controls in the top bar, because they are
// changed in passing. These six are changed once and then forgotten, so they
// get a screen rather than a header control - and a screen anybody may open,
// which is the point: until now they were the instance's, and only an
// administrator could reach them.

// handlePreferences renders the acting user's own presentation choices.
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "preferences")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("preferences.title")

	// The instance's own values, unpersonalised, so the screen can say what
	// each choice falls back to. data.Settings has already been overlaid with
	// this person's choices, which is the right thing everywhere else and the
	// wrong thing here: a form that showed the resolved value could not tell
	// "I chose 24-hour" from "the instance did".
	if data.InstanceDefaults, err = s.svc.Settings(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_preferences.html", data)
}

// handleUpdatePreferences saves them.
func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	// An absent or unparseable hour is "no preference", which is -1 rather
	// than 0 - midnight is a legitimate hour for somebody to start at.
	hour := func(name string) int {
		value, err := strconv.Atoi(r.FormValue(name))
		if err != nil || value < 0 || value > 23 {
			return -1
		}
		return value
	}

	err := s.svc.UpdatePresentation(r.Context(), service.PresentationInput{
		ClockFormat:  r.FormValue("clock_format"),
		DateFormat:   r.FormValue("date_format"),
		NavPosition:  r.FormValue("nav_position"),
		DayOverflow:  r.FormValue("day_overflow"),
		DayStartHour: hour("day_start_hour"),
		DayEndHour:   hour("day_end_hour"),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/preferences", http.StatusSeeOther)
}
