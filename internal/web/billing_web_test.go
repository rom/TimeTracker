package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The billing screen, over HTTP.
//
// The service tests prove the rules. This proves the screen reaches them, in
// the order somebody actually uses it: look at what would go out, record it,
// find the time refusing to change, void the run, find it changeable again.

func TestBillingShowsWhatWouldGoOutAndThenRecordsIt(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// A billable hour, dated recently enough to sit in a range the screen can
	// be asked for.
	day := time.Now().UTC().AddDate(0, 0, -3)
	iso := day.Format("2006-01-02")
	rec := post(t, srv, "/entries", url.Values{
		"assignment_id":   {"1"},
		"date":            {iso},
		"start":           {"09:00"},
		"duration":        {"2h"},
		"note":            {"Migration work"},
		"billable_source": {"assignment"},
	})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("POST /entries = %d: %s", rec.Code, rec.Body.String())
	}

	from := day.AddDate(0, 0, -1).Format("2006-01-02")
	to := day.AddDate(0, 0, 1).Format("2006-01-02")
	show := get(t, srv, "/billing?customer=1&from="+from+"&to="+to)
	if show.Code != http.StatusOK {
		t.Fatalf("GET /billing = %d: %s", show.Code, show.Body.String())
	}
	if !strings.Contains(show.Body.String(), "Migration work") &&
		!strings.Contains(show.Body.String(), "Development") {
		t.Error("the screen does not show the entries behind the figure, " +
			"which is the half that makes the figure checkable")
	}

	commit := post(t, srv, "/billing", url.Values{
		"customer":  {"1"},
		"from":      {from},
		"to":        {to},
		"reference": {"INV-2026-014"},
	})
	if commit.Code != http.StatusSeeOther && commit.Code != http.StatusOK {
		t.Fatalf("POST /billing = %d: %s", commit.Code, commit.Body.String())
	}

	history := get(t, srv, "/billing").Body.String()
	if !strings.Contains(history, "INV-2026-014") {
		t.Error("the run is not in the history")
	}

	// Billed, so no longer offered.
	again := get(t, srv, "/billing?customer=1&from="+from+"&to="+to).Body.String()
	if !strings.Contains(again, "Nothing left to invoice") {
		t.Error("the same hours are still offered for billing after being billed")
	}

	// And the time refuses to change under the invoice.
	edit := post(t, srv, "/entries/1", url.Values{
		"assignment_id": {"1"}, "date": {iso},
		"start": {"09:00"}, "duration": {"3h"},
	})
	if edit.Code == http.StatusSeeOther || edit.Code == http.StatusOK {
		t.Error("billed time was edited; the invoice now says something else")
	}

	// Voiding puts it back.
	void := post(t, srv, "/billing/1/void", url.Values{"confirmed": {"1"}})
	if void.Code != http.StatusSeeOther && void.Code != http.StatusOK {
		t.Fatalf("POST /billing/1/void = %d: %s", void.Code, void.Body.String())
	}
	edit = post(t, srv, "/entries/1", url.Values{
		"assignment_id": {"1"}, "date": {iso},
		"start": {"09:00"}, "duration": {"3h"},
	})
	if edit.Code != http.StatusSeeOther && edit.Code != http.StatusOK {
		t.Errorf("editing after voiding the run = %d: %s", edit.Code, edit.Body.String())
	}
}

func TestBillingNothingIsRefusedRatherThanRecorded(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// No time recorded at all, so there is nothing to invoice. A run written
	// here would make a period that was never billed read as already invoiced.
	rec := post(t, srv, "/billing", url.Values{
		"customer": {"1"}, "from": {"2026-03-01"}, "to": {"2026-03-31"},
	})
	if rec.Code == http.StatusSeeOther {
		t.Error("an empty billing run was recorded")
	}
	if !strings.Contains(get(t, srv, "/billing").Body.String(), "Nothing has been billed") {
		t.Error("the history is not empty after a refused run")
	}
}
