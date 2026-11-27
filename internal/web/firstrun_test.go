package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// What a brand-new instance says.
//
// It used to say nothing. The quick-add box carried a worked example naming a
// customer and a project that did not exist, so the first thing anybody typed
// came back "could not read that: no assignment matched" - a true statement
// about the parser and a useless one about the situation. Beside it were two
// more controls that could only fail: copy yesterday, with no yesterday to
// copy, and a manual entry form whose assignment list was empty.
//
// The setup guide that answers all of this was already written and translated.
// Nothing linked to it.

// TestANewInstanceSaysWhatIsMissing.
func TestANewInstanceSaysWhatIsMissing(t *testing.T) {
	srv, _ := newTestServer(t)

	body := get(t, srv, "/today").Body.String()

	// What is missing, and the two ways to fix it.
	if !strings.Contains(body, "Nothing to record against yet") {
		t.Error("a new instance does not say that it has no catalogue")
	}
	if !strings.Contains(body, `href="/admin"`) {
		t.Error("nothing on the first screen leads to where a customer is created")
	}
	if !strings.Contains(body, `href="/guide/setup"`) {
		t.Error("nothing links to the setup guide, which was written for exactly " +
			"this moment and reachable only by knowing it was there")
	}

	// And the controls that could only have failed are not offered.
	for _, dead := range []struct{ markup, what string }{
		{`action="/entries/quick"`, "quick add, whose example names a catalogue that does not exist"},
		{`action="/days/copy`, "copy yesterday, with no yesterday to copy"},
		{`class="manual-entry"`, "the manual entry form, whose assignment list is empty"},
	} {
		if strings.Contains(body, dead.markup) {
			t.Errorf("a new instance still offers %s", dead.what)
		}
	}
}

// TestTheScreenComesBackOnceThereIsSomethingToRecordAgainst.
//
// The other half: the guard is on the catalogue, not on the day being empty.
// An instance with assignments and an empty Tuesday is not a new instance, and
// hiding the quick add there would be a far worse bug than the one being fixed.
func TestTheScreenComesBackOnceThereIsSomethingToRecordAgainst(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// A day with nothing on it, deliberately.
	body := get(t, srv, "/today?date=2026-03-17").Body.String()

	for _, live := range []struct{ markup, what string }{
		{`action="/entries/quick"`, "quick add"},
		{`action="/days/copy`, "copy yesterday"},
		{`class="manual-entry"`, "the manual entry form"},
	} {
		if !strings.Contains(body, live.markup) {
			t.Errorf("an empty day on a set-up instance does not offer %s", live.what)
		}
	}
	if strings.Contains(body, "Nothing to record against yet") {
		t.Error("an empty day is being reported as an empty instance")
	}
}

// TestANewInstanceIsNotMonochrome.
//
// The palette is seven colours, contrast-tested in eight themes, and carried by
// the chips, the week grid, the month calendar and every timeline block. It
// defaulted to the neutral key, so it showed one colour until somebody set each
// customer by hand - and almost nobody does, which made the feature that lets a
// dense screen be read at a glance off by default.
func TestANewInstanceIsNotMonochrome(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, name := range []string{"Acme", "Nordgren", "Vasa", "Kettil"} {
		post(t, srv, "/customers", url.Values{
			"name": {name}, "currency": {"SEK"}, "rate": {"1000"},
		})
	}

	body := get(t, srv, "/admin").Body.String()

	// Four customers, four different colours. Asserted as "more than one"
	// rather than naming them: which colour is third is not a promise, and a
	// test that said so would break every time the palette was reordered.
	seen := map[string]bool{}
	for _, key := range []string{"blue", "green", "amber", "red", "purple", "teal", "slate"} {
		if strings.Contains(body, `data-colour="`+key+`"`) {
			seen[key] = true
		}
	}
	if len(seen) < 2 {
		t.Errorf("four customers drew %d distinct colours; a new instance is "+
			"still monochrome", len(seen))
	}
}

// TestSearchIsReachableFromEveryScreen.
//
// Trigram search with a scan fallback and optional regexp has existed since
// ADR-0029, reachable from one filter box on one screen. The strongest way to
// find something in a long history was the hardest thing in the interface to
// find.
func TestSearchIsReachableFromEveryScreen(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	for _, screen := range []string{"/today", "/week", "/month", "/entries", "/admin", "/settings"} {
		body := get(t, srv, screen).Body.String()
		if !strings.Contains(body, `class="topbar-search"`) {
			t.Errorf("%s offers no way to search", screen)
		}
	}
}

// TestAHeaderSearchLooksPastTheLastThirtyDays.
//
// The entries screen defaults to the last thirty days, and that default is
// invisible from the top bar. A header search for something recorded in March
// coming back empty in May - with nothing on the screen to say a date range had
// excluded it - would be the same failure ADR-0029 refuses at the other end,
// where a query too short for the index says so rather than returning nothing.
func TestAHeaderSearchLooksPastTheLastThirtyDays(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// Well outside the default window, which is thirty days back from the
	// fixture's clock.
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2025-06-11"}, "start": {"09:00"},
		"duration": {"2h"}, "note": {"archaeology"}, "billable": {"on"},
	})

	// Looked for in the results, not on the page: the query is echoed back into
	// the search box, so the word is present either way and a naive substring
	// check passes whatever happened.
	if !strings.Contains(resultsTable(t, srv, "/entries?all=1&q=archaeology"), "archaeology") {
		t.Error("a search from the header missed an entry outside the last " +
			"thirty days, which is the whole reason it sends all=1")
	}

	// And the screen's own default is still the last thirty days, so this did
	// not quietly widen every listing.
	if strings.Contains(resultsTable(t, srv, "/entries?q=archaeology"), "archaeology") {
		t.Error("the entries screen now searches all time by default; the wide " +
			"range is meant to be what the header asks for, not the default")
	}
}

// resultsTable returns just the listing from the entries screen.
//
// Everything else on that page - the filter boxes, the search field, the export
// links - carries the query string back to the user, so asserting against the
// whole body says only that the request was made.
func resultsTable(t *testing.T, srv *Server, path string) string {
	t.Helper()

	body := get(t, srv, path).Body.String()
	at := strings.Index(body, `<table class="entries"`)
	if at < 0 {
		// No table at all is a legitimate answer: nothing matched.
		return ""
	}
	end := strings.Index(body[at:], "</table>")
	if end < 0 {
		t.Fatalf("the entries table is never closed on %s", path)
	}
	return body[at : at+end]
}

// TestDeletingWithoutJavaScriptAsksFirst.
//
// hx-confirm was the only confirmation the interface had, and it is a
// window.confirm inside a script: it disappears along with the script, so
// without JavaScript a single click deleted. The script now reports that it
// asked, by sending confirmed=1, and the server asks when it did not.
func TestDeletingWithoutJavaScriptAsksFirst(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// Today's date, because this server runs on the real clock and the entries
	// listing defaults to the last thirty days: an entry dated in a fixed past
	// would be created successfully and then be invisible to the assertions
	// below, which would read as a deletion that had already happened.
	today := time.Now().UTC().Format("2006-01-02")
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"2h"}, "note": {"not deleted yet"}, "billable": {"on"},
	})

	// What a browser with no script sends: the form, and nothing else.
	asked := post(t, srv, "/entries/1/delete", url.Values{})
	if asked.Code != http.StatusOK {
		t.Fatalf("deleting without an answer = %d, want the question", asked.Code)
	}
	body := asked.Body.String()
	if !strings.Contains(body, "Are you sure?") {
		t.Error("the second ask does not ask anything")
	}
	// Naming the record, because "delete this entry?" cannot be answered from a
	// page that does not say which entry.
	if !strings.Contains(body, "not deleted yet") {
		t.Errorf("the question does not say what would be deleted:\n%s", body)
	}
	// And it says the deletion is recoverable, which is the honest thing to
	// say now that it is.
	if !strings.Contains(body, "Recently deleted") {
		t.Error("the question does not say the deletion can be undone")
	}

	// Nothing happened yet.
	if !strings.Contains(resultsTable(t, srv, "/entries"), "not deleted yet") {
		t.Fatal("the entry was deleted by the request that only asked about it")
	}

	// Answering deletes.
	if done := post(t, srv, "/entries/1/delete", url.Values{"confirmed": {"1"}}); done.Code >= 400 {
		t.Fatalf("confirming the deletion = %d", done.Code)
	}
	if strings.Contains(resultsTable(t, srv, "/entries"), "not deleted yet") {
		t.Error("the entry survived a confirmed deletion")
	}

	// And it is recoverable.
	bin := get(t, srv, "/deleted").Body.String()
	if !strings.Contains(bin, "not deleted yet") {
		t.Error("the deleted entry is not in the recycle bin")
	}
}
