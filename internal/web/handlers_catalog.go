package web

import (
	"net/http"

	"github.com/rom/timetracker/internal/domain"
)

// handleAdmin renders the catalogue administration screen: customers, projects
// and assignments, with their colours and icons.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "Admin", "admin")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Archived records are included here, and only here: this is the screen where
	// someone would go to restore one.
	if data.Customers, err = s.svc.Customers(r.Context(), true); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Projects, err = s.svc.Projects(r.Context(), 0, true); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, true); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_admin.html", data)
}

// handleCreateCustomer adds a customer.
func (s *Server) handleCreateCustomer(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, err = s.svc.CreateCustomer(r.Context(), domain.Customer{
		Name:      r.FormValue("name"),
		Code:      r.FormValue("code"),
		Currency:  r.FormValue("currency"),
		ColourKey: r.FormValue("colour_key"),
		Icon:      r.FormValue("icon"),
		RateMinor: rate,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleArchiveCustomer retires a customer, or restores one.
//
// There is no delete. Removing a customer would leave last year's invoices unable
// to say who was billed, so the catalogue only ever archives.
func (s *Server) handleArchiveCustomer(w http.ResponseWriter, r *http.Request) {
	archived := r.FormValue("restore") == ""
	if err := s.svc.SetCustomerArchived(r.Context(), int64Param(r.PathValue("id")), archived); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleCreateProject adds a project to a customer.
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, err = s.svc.CreateProject(r.Context(), domain.Project{
		CustomerID:      int64Param(r.FormValue("customer_id")),
		Name:            r.FormValue("name"),
		Code:            r.FormValue("code"),
		ColourKey:       r.FormValue("colour_key"),
		Icon:            r.FormValue("icon"),
		BillableDefault: r.FormValue("billable") != "",
		RateMinor:       rate,
		RoundingRule:    r.FormValue("rounding_rule"),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleArchiveProject retires a project, or restores one.
func (s *Server) handleArchiveProject(w http.ResponseWriter, r *http.Request) {
	archived := r.FormValue("restore") == ""
	if err := s.svc.SetProjectArchived(r.Context(), int64Param(r.PathValue("id")), archived); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleCreateAssignment adds an assignment - the thing a timer runs against.
func (s *Server) handleCreateAssignment(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, err = s.svc.CreateAssignment(r.Context(), domain.Assignment{
		ProjectID:       int64Param(r.FormValue("project_id")),
		Name:            r.FormValue("name"),
		Code:            r.FormValue("code"),
		ColourKey:       r.FormValue("colour_key"),
		Icon:            r.FormValue("icon"),
		BillableDefault: r.FormValue("billable") != "",
		RateMinor:       rate,
		Favourite:       r.FormValue("favourite") != "",
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleArchiveAssignment retires an assignment, or restores one.
func (s *Server) handleArchiveAssignment(w http.ResponseWriter, r *http.Request) {
	archived := r.FormValue("restore") == ""
	if err := s.svc.SetAssignmentArchived(r.Context(), int64Param(r.PathValue("id")), archived); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleSetTheme stores the user's theme choice.
//
// The browser applies it immediately without waiting for this to return; the
// round trip exists so the choice follows the person to another device, and so
// the correct theme is applied server-side on the next first paint rather than
// flashing the default.
func (s *Server) handleSetTheme(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	theme := r.FormValue("theme")

	// Only a known theme is accepted. The value ends up in an HTML attribute, so
	// an allow-list here is both a correctness and a safety measure.
	valid := false
	for _, known := range availableThemes {
		if theme == known {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "Unknown theme.", http.StatusBadRequest)
		return
	}

	if err := s.svc.SetTheme(r.Context(), theme); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseRate reads an hourly rate from a form field into minor units.
func parseRate(raw, currency string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	money, err := domain.ParseMoney(raw, currency)
	if err != nil {
		return 0, domainValidation("could not read the rate: " + err.Error())
	}
	if money.Minor < 0 {
		return 0, domainValidation("a rate cannot be negative")
	}
	return money.Minor, nil
}
