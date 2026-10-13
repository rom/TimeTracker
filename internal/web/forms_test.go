package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Reading a form: the shapes a browser and a person can produce.
//
// Every write begins by parsing the body, and every refusal below has to say
// what to fix rather than failing as a server fault. An entry form that was
// rejected with "something went wrong" sends somebody to look for a bug in the
// application when what they typed was "1 hour".

// TestAnUnreadableBodyIsTheCallersMistake.
//
// A percent-escape that is not one makes the body unparseable. Nothing a form
// submits looks like this, so in practice it is a hand-made request or a
// mangled one - and either way it is a 400, not a 500. A 500 would be logged as
// an application fault and would page somebody.
//
// Swept across every write, because it is the same first three lines in each
// handler and the thing worth catching is one that forgets them.
func TestAnUnreadableBodyIsTheCallersMistake(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// "%zz" is not a valid escape, so the body cannot be decoded at all.
	const unreadable = "note=%zz&assignment_id=1"

	// Some routes act entirely on their path - stop every timer, archive this
	// one - and never look at the body. An unreadable body is not a mistake
	// they can make, so they go through as usual, which is correct.
	ignoresTheBody := map[string]bool{
		"/timers/stop-all": true, "/customers/1/archive": true,
		"/projects/1/archive": true, "/assignments/1/archive": true,
	}

	refused := 0
	for _, write := range writes {
		// The upload routes read multipart bodies and are swept by their own
		// tests; a URL-encoded body is not a mistake they can make either.
		if strings.HasPrefix(write.path, "/import") || strings.HasPrefix(write.path, "/calendar") {
			continue
		}
		t.Run(write.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, write.path, strings.NewReader(unreadable))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code == http.StatusInternalServerError {
				t.Errorf("POST %s with an unreadable body = 500; an unparseable "+
					"body is the caller's mistake and would be logged as ours", write.path)
			}
			if rec.Code >= 400 {
				refused++
				return
			}
			if !ignoresTheBody[write.path] {
				t.Errorf("POST %s with an unreadable body = %d - a route that "+
					"reads its form must refuse one it cannot read", write.path, rec.Code)
			}
		})
	}

	if refused == 0 {
		t.Error("nothing refused an unreadable body, so this test is not reaching the handlers")
	}
}

// TestWorkThatRanPastMidnight.
//
// An end time before the start is not a mistake: it is the small hours. The
// alternative reading - refusing it, or storing a negative duration - would
// make the application unusable for anybody who works late, and a negative
// duration would quietly subtract from a week's total.
func TestWorkThatRanPastMidnight(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"},
		"start": {"22:00"}, "end": {"01:30"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record work past midnight = %d: %s", rec.Code, rec.Body.String())
	}

	// Three and a half hours, counted on the day it started.
	if got := totalOn(t, srv, "2026-03-16"); got != "3h 30m" {
		t.Errorf("the night's work totals %q, want 3h 30m", got)
	}

	// An end equal to the start is the same reading: a full twenty-four hours
	// rather than nothing at all.
	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-18"},
		"start": {"09:00"}, "end": {"09:00"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record a day-long entry = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-18"); got == noTime {
		t.Error("an entry whose end equals its start recorded nothing")
	}
}

// TestAnEntryFormSaysWhatToFix.
//
// Each of these is something somebody types. The message has to name the value,
// because a form with a dozen fields gives no clue which one was wrong.
func TestAnEntryFormSaysWhatToFix(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	cases := []struct {
		name string
		form url.Values
		says string
	}{
		{"a kind of time that does not exist",
			url.Values{"assignment_id": {"1"}, "kind": {"sleeping"}, "duration": {"1h"}},
			"sleeping"},
		{"a date that is not one",
			url.Values{"assignment_id": {"1"}, "date": {"last Tuesday"}, "duration": {"1h"}},
			"date and time"},
		{"a start time that is not one",
			url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
				"start": {"half nine"}, "duration": {"1h"}},
			"date and time"},
		{"an end time that is not one",
			url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
				"start": {"09:00"}, "end": {"teatime"}},
			"end time"},
		{"a duration in words",
			url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
				"start": {"09:00"}, "duration": {"about an hour"}},
			"about an hour"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/entries", "/entries/1"} {
				rec := post(t, srv, path, tc.form)
				if rec.Code == http.StatusSeeOther {
					t.Errorf("POST %s accepted %v", path, tc.form)
					continue
				}
				if rec.Code == http.StatusInternalServerError {
					t.Errorf("POST %s = 500 for %v, want a refusal", path, tc.form)
					continue
				}
				if !strings.Contains(rec.Body.String(), tc.says) {
					t.Errorf("POST %s does not mention %q:\n%s",
						path, tc.says, truncate(rec.Body.String()))
				}
			}
		})
	}

	// The duration refusal also shows what a duration looks like, since
	// knowing the field is wrong is not the same as knowing what to put in it.
	rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "duration": {"about an hour"},
	})
	for _, example := range []string{"1h30", "90m"} {
		if !strings.Contains(rec.Body.String(), example) {
			t.Errorf("the duration refusal does not show the example %q", example)
		}
	}

	// And nothing was recorded by any of them.
	if got := totalOn(t, srv, "2026-03-16"); got != noTime {
		t.Errorf("a refused entry form recorded %q", got)
	}
}

// TestDatesAreReadAndWrittenInTheUsersOwnZone.
//
// A date in a form is a local date. Getting this wrong puts an entry on the
// wrong calendar day, which is the kind of error nobody spots until an invoice
// is queried - so the fallbacks matter as much as the normal path: a user with
// no zone set, or with one this machine has never heard of, has to land
// somewhere defined rather than on a nil location.
func TestDatesAreReadAndWrittenInTheUsersOwnZone(t *testing.T) {
	for _, zone := range []string{"Europe/Stockholm", "", "Mars/Olympus_Mons"} {
		name := zone
		if name == "" {
			name = "no zone set at all"
		}
		t.Run(name, func(t *testing.T) {
			srv, _ := newTestServer(t)
			seedCatalogue(t, srv)
			if rec := post(t, srv, "/settings", url.Values{
				"currency": {"SEK"}, "week_start": {"1"},
			}); rec.Code != http.StatusSeeOther {
				t.Fatalf("settings = %d", rec.Code)
			}
			setZone(t, srv, zone)

			// Late in the evening, local time: the entry belongs to that local
			// day whatever the zone, which is the whole point.
			if rec := post(t, srv, "/entries", url.Values{
				"assignment_id": {"1"}, "date": {"2026-03-16"},
				"start": {"23:30"}, "duration": {"30m"},
			}); rec.Code != http.StatusSeeOther {
				t.Fatalf("record = %d: %s", rec.Code, rec.Body.String())
			}
			if got := totalOn(t, srv, "2026-03-16"); got != "30m" {
				t.Errorf("the entry totals %q on the day it was entered for, want 30m", got)
			}
			if got := totalOn(t, srv, "2026-03-17"); got != noTime {
				t.Errorf("the entry also appears on the next day, totalling %q", got)
			}
		})
	}
}

// setZone puts the acting user in a time zone, including one that does not
// exist - which is what a hand-edited database or a renamed zone looks like.
func setZone(t *testing.T, srv *Server, zone string) {
	t.Helper()
	if rec := post(t, srv, "/preferences/language", url.Values{
		"language": {"en"}, "timezone": {zone},
	}); rec.Code != http.StatusSeeOther && rec.Code != http.StatusNoContent {
		t.Fatalf("set the zone to %q = %d: %s", zone, rec.Code, rec.Body.String())
	}
}

// TestAClientIsSentToTheListingRatherThanTheirOwnTimesheet.
//
// The day, week and month screens are about somebody's own timesheet, and a
// client has none: they are looking at work done for them, by other people. A
// month grid of empty squares with their own name on it is a worse answer than
// the listing that does have something in it.
//
// All of them send a client to that listing now. The week and the submission
// review used to refuse outright, so a client following a week link from
// anywhere got a bare "not permitted" where the day and month links took them
// somewhere useful - the day and month had the redirect and the week never did,
// because it was the same four lines copied twice rather than one helper.
func TestAClientIsSentToTheListingRatherThanTheirOwnTimesheet(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)

	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}})
	admin.post(t, "/users", url.Values{
		"display_name": {"Acme's Reviewer"}, "email": {"reviewer@example.com"},
		"password": {"a-long-enough-password"}, "role": {"client"},
		"client_customer_id": {"1"},
	})

	reviewer := signInAsReadOnly(t, srv, accounts, "reviewer@example.com", "a-long-enough-password")

	for _, path := range []string{"/month", "/month?month=2026-03"} {
		rec := reviewer.get(t, path)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("a client's GET %s = %d, want a redirect\n%s",
				path, rec.Code, truncate(rec.Body.String()))
		}
		if target := rec.Header().Get("Location"); !strings.HasPrefix(target, "/entries") {
			t.Errorf("a client asking for %s lands on %q, want the listing", path, target)
		}
	}

	// The listing they are sent to does work for them, so the redirect is not
	// a loop or a dead end.
	if rec := reviewer.get(t, "/entries"); rec.Code != http.StatusOK {
		t.Errorf("the listing a client is sent to = %d, want 200\n%s",
			rec.Code, truncate(rec.Body.String()))
	}

	// Every screen about their own time answers the same way, including the
	// week with a date on it and the review in front of a submission they
	// cannot make.
	for _, path := range []string{"/", "/today", "/today?date=2026-03-16",
		"/week", "/week?date=2026-03-16", "/week/review"} {
		rec := reviewer.get(t, path)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("a client's GET %s = %d, want the same redirect the month "+
				"gives\n%s", path, rec.Code, truncate(rec.Body.String()))
			continue
		}
		if target := rec.Header().Get("Location"); !strings.HasPrefix(target, "/entries") {
			t.Errorf("a client asking for %s lands on %q, want the listing", path, target)
		}
	}

	// A screen that is administrative rather than personal still refuses
	// outright, which is the right answer there: it has no client equivalent to
	// be sent to, and a redirect would claim it does.
	for _, path := range []string{"/approvals", "/users", "/backup", "/admin"} {
		if rec := reviewer.get(t, path); rec.Code != http.StatusForbidden {
			t.Errorf("a client's GET %s = %d, want 403", path, rec.Code)
		}
	}
}

// TestASavedEntryTellsAScriptedClientWhereToGo.
//
// An edit is reached as its own page, so after saving there is nowhere to swap
// a fragment into - the browser has to navigate. HX-Redirect is how that is
// said to a scripted client; without it the form would appear to do nothing.
func TestASavedEntryTellsAScriptedClientWhereToGo(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"2h"},
	})

	rec := postHTMX(srv, "/entries/1", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"},
		"start": {"10:00"}, "duration": {"3h"}, "next": {"/entries"},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("saving an edit as a scripted client = %d: %s",
			rec.Code, truncate(rec.Body.String()))
	}
	if target := rec.Header().Get("HX-Redirect"); target == "" {
		t.Error("saving an edit did not tell the client where to go, so the form " +
			"would appear to do nothing")
	}

	// The edit took, whichever way the client was told about it.
	if got := totalOn(t, srv, "2026-03-16"); got != "3h 00m" {
		t.Errorf("the day totals %q after the edit, want 3h 00m", got)
	}

}
