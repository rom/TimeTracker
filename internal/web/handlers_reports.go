package web

import (
	"net/http"
	"time"

	"github.com/rom/timetracker/internal/service"
)

// handleReports renders the summary: where the time went over a range, grouped.
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	// A client has one screen, and it is their own listing. The figures here
	// cross customers by construction.
	if s.sentToTheirOwnListing(w, r) {
		return
	}

	data, err := s.newPageData(r, "", "reports")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("reports.title")

	from, to := reportRange(r, s.svc.Now())
	group := service.GroupBy(r.URL.Query().Get("group")).OrDefault()

	report, err := s.svc.Summarise(r.Context(), from, to, group)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Summary = report
	data.Groupings = service.Groupings

	s.render(w, r, "page_reports.html", data)
}

// reportRange reads the range, defaulting to the month in progress.
//
// The month rather than the last thirty days the entries screen uses: a summary
// is read to answer a question about a period somebody can name, and "this
// month so far" is nameable in a way that "since the 28th of last month" is not.
func reportRange(r *http.Request, now time.Time) (time.Time, time.Time) {
	from, err := time.Parse("2006-01-02", r.URL.Query().Get("from"))
	if err != nil {
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	to, err := time.Parse("2006-01-02", r.URL.Query().Get("to"))
	if err != nil {
		to = now
	}
	// A range that runs backwards is a typed URL rather than a request for
	// anything; the empty report it would produce says nothing useful.
	if to.Before(from) {
		from, to = to, from
	}
	return from, to
}
