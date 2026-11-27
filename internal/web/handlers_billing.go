package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rom/timetracker/internal/i18n"
	"github.com/rom/timetracker/internal/service"
)

// What has been billed.
//
// Two passes, like every other screen here that writes in bulk: choose a
// customer and a range and see exactly what would go out, then record it. The
// first pass is the point - the figure on an invoice is the one people query
// afterwards, and seeing the forty entries behind it beforehand is what makes
// it checkable.

// handleBilling shows the billing screen: what is still to invoice for the
// chosen customer, and what has been billed already.
func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	if s.sentToTheirOwnListing(w, r) {
		return
	}

	data, err := s.newPageData(r, "", "billing")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("billing.title")

	if data.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}

	// The history, which is the half of the screen that is worth opening on
	// its own: "did we bill March" is asked more often than March is billed.
	runs, err := s.svc.BillingRuns(r.Context(), 0, 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.BillingRuns = runs

	customerID := int64Param(r.URL.Query().Get("customer"))
	if customerID == 0 {
		// Nothing chosen yet: the history alone, and the form above it.
		s.render(w, r, "page_billing.html", data)
		return
	}

	from, to := billingRange(r, s.svc.Now())
	data.BillingFrom = from.Format("2006-01-02")
	data.BillingTo = to.Format("2006-01-02")

	candidates, err := s.svc.BillingCandidatesFor(r.Context(), customerID, from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.BillingCandidates = &candidates
	s.render(w, r, "page_billing.html", data)
}

// handleRecordBilling writes the run.
func (s *Server) handleRecordBilling(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	run, err := s.svc.RecordBillingRun(r.Context(), service.BillingInput{
		CustomerID: int64Param(r.FormValue("customer")),
		StartsOn:   r.FormValue("from"),
		EndsOn:     r.FormValue("to"),
		Reference:  r.FormValue("reference"),
		Note:       r.FormValue("note"),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/billing?run="+fmt.Sprint(run.ID), http.StatusSeeOther)
}

// handleVoidBilling cancels a run and releases its entries.
func (s *Server) handleVoidBilling(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	id := int64Param(r.PathValue("id"))
	run, readErr := s.svc.BillingRunSummary(r.Context(), id)
	describe := func(p *i18n.Printer) string {
		if readErr != nil {
			return ""
		}
		what := run.CustomerName + " · " + run.StartsOn + "–" + run.EndsOn
		if run.Reference != "" {
			what += " · " + run.Reference
		}
		return what
	}
	// Voiding releases the time, which is the opposite of destructive - but it
	// unsays something said to a customer, so it is confirmed like a deletion.
	if s.needsConfirming(w, r, describe, "/billing") {
		return
	}

	if err := s.svc.VoidBillingRun(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/billing", http.StatusSeeOther)
}

// billingRange reads the days to bill, defaulting to last month.
//
// Last rather than this: billing happens after a period ends, so the range
// somebody opens this screen for is almost always the one that just closed.
func billingRange(r *http.Request, now time.Time) (time.Time, time.Time) {
	const iso = "2006-01-02"
	lastMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)

	from, err := time.Parse(iso, r.URL.Query().Get("from"))
	if err != nil {
		from = lastMonth
	}
	to, err := time.Parse(iso, r.URL.Query().Get("to"))
	if err != nil {
		to = lastMonth.AddDate(0, 1, -1)
	}
	return from, to
}
