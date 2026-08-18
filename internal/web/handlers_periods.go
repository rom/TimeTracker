package web

import (
	"net/http"
	"time"
)

// Weekly submit and approve.

// handleSubmitWeek declares a week finished.
func (s *Server) handleSubmitWeek(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.svc.SubmitWeek(r.Context(), s.weekParam(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleWithdrawWeek takes back an undecided submission.
func (s *Server) handleWithdrawWeek(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.WithdrawWeek(r.Context(), s.weekParam(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleApprovals renders the approval queue.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "approvals")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("approvals.title")

	if data.Approvals, err = s.svc.PendingApprovals(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	// Approved weeks are listed too: reopening one is only possible if it can
	// be found, and an approved week has left the pending queue by definition.
	if data.Approved, err = s.svc.ApprovedPeriods(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.MyPeriods, err = s.svc.RecentPeriods(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_approvals.html", data)
}

// handleApproveWeek accepts somebody else's week.
func (s *Server) handleApproveWeek(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.svc.ApproveWeek(r.Context(),
		int64Param(r.FormValue("user_id")), r.FormValue("week_start"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleRejectWeek sends a week back with a reason.
func (s *Server) handleRejectWeek(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.svc.RejectWeek(r.Context(),
		int64Param(r.FormValue("user_id")), r.FormValue("week_start"), r.FormValue("reason"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleReopenWeek unlocks an approved week.
func (s *Server) handleReopenWeek(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.svc.ReopenWeek(r.Context(),
		int64Param(r.FormValue("user_id")), r.FormValue("week_start"), r.FormValue("reason"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// weekParam reads the week a request is about, defaulting to the current one.
func (s *Server) weekParam(r *http.Request) time.Time {
	if raw := r.FormValue("week"); raw != "" {
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			return parsed
		}
	}
	return s.dateParam(r, "date")
}
