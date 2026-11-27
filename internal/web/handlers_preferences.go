package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rom/timetracker/internal/domain"

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
		// Blank means "follow the instance", which is -1 rather than 0: zero
		// is a legitimate choice meaning "do not compare my week to anything".
		ExpectedWeeklyMinutes: minutesFromDuration(r.FormValue("expected_week"), -1),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/preferences", http.StatusSeeOther)
}

// minutesFromDuration reads a full working week as whole minutes.
//
// Through the application's own duration parser rather than as a number of
// hours, which is what the first version did - and which tripped the rule that
// money and durations are integers throughout (ADR-0014). A working week is
// not always a whole number of hours; 37.5 is the common one in several
// countries, and "37.5" through a float is exactly the arithmetic that rule
// exists to keep out of a timesheet.
//
// The parser already understands "37h30m", "37.5h" and "37:30" and answers in
// whole seconds, so the form can accept all three and nothing has to divide
// anything. An unreadable or absent value gives the caller's "unset" figure
// rather than zero, because zero means something specific here: do not compare
// this week to anything.
func minutesFromDuration(value string, unset int) int {
	if strings.TrimSpace(value) == "" {
		return unset
	}
	seconds, err := domain.ParseDuration(value)
	if err != nil || seconds < 0 || seconds > 7*24*3600 {
		return unset
	}
	return int(seconds / 60)
}
