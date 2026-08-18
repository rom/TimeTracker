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

// handleApprovalReport renders approval status per person per week.
//
// Separate from the queue because it answers a different question. The queue is
// "what is waiting for me"; this is "who has not submitted", which is the
// absence of a submission rather than one in a particular state - so the cells
// worth looking at are the ones that would otherwise be blank.
func (s *Server) handleApprovalReport(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "approvals")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("report.approval.title")

	weeks := int(int64Param(r.URL.Query().Get("weeks")))
	if weeks <= 0 {
		weeks = defaultReportWeeks
	}
	data.ReportWeeks = weeks

	report, err := s.svc.ApprovalReportFor(r.Context(), s.dateParam(r, "date"), weeks)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.ApprovalReport = &report
	// The service clamps the range; the form has to show what was actually
	// used rather than what was asked for, or the selector lies.
	data.ReportWeeks = len(report.Weeks)

	s.render(w, r, "page_approval_report.html", data)
}

// defaultReportWeeks is a quarter, which is the span most people review over.
const defaultReportWeeks = 12
