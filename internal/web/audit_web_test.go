package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Reading the trail, over HTTP.
//
// The service tests prove the rules and the bounds. These prove the two
// surfaces exist, which is the whole finding: every mutation has written a row
// since the first migration and until now no page showed one.

func TestTheHistoryScreenShowsWhatHappened(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	iso := time.Now().UTC().Format("2006-01-02")
	rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {iso}, "start": {"09:00"},
		"duration": {"2h"}, "note": {"Recorded work"},
	})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("POST /entries = %d", rec.Code)
	}

	page := get(t, srv, "/audit")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /audit = %d: %s", page.Code, page.Body.String())
	}
	body := page.Body.String()
	for _, want := range []string{"time_entry.create", "customer.create"} {
		if !strings.Contains(body, want) {
			t.Errorf("the history does not mention %q", want)
		}
	}

	// Filtering narrows it, and the filtered view has a URL of its own. Read
	// from the listing rather than the whole page: the chosen action is
	// echoed into the filter's own select, so the page contains it either way.
	filtered := resultsTable(t, srv, "/audit?action=time_entry.create")
	if !strings.Contains(filtered, "time_entry.create") {
		t.Error("filtering for an action that happened showed nothing")
	}
	if strings.Contains(filtered, "customer.create") {
		t.Error("filtering for one action listed another")
	}
}

func TestAnEntryCarriesItsHistoryOnItsOwnScreen(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	iso := time.Now().UTC().Format("2006-01-02")
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {iso}, "start": {"09:00"},
		"duration": {"2h"},
	})
	post(t, srv, "/entries/1", url.Values{
		"assignment_id": {"1"}, "date": {iso}, "start": {"09:00"},
		"duration": {"3h"},
	})

	form := get(t, srv, "/entries/1/edit")
	if form.Code != http.StatusOK {
		t.Fatalf("GET /entries/1/edit = %d", form.Code)
	}
	body := form.Body.String()
	if !strings.Contains(body, "entry-history") {
		t.Fatal("the correction form has no history panel")
	}
	for _, want := range []string{"time_entry.create", "time_entry.update"} {
		if !strings.Contains(body, want) {
			t.Errorf("the entry's history does not mention %q, so \"who "+
				"changed this\" is still unanswerable beside the record", want)
		}
	}
}
