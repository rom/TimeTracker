package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The leave screen, over HTTP.
//
// The service tests prove the rules; these prove the screen reaches them - the
// form that doubles as a correction form, and the calendar import reading the
// same file as days away rather than as time.

// recordAbsence puts one day in the register and returns the listing.
func recordAbsence(t *testing.T, srv *Server, kind, from, to, note string) string {
	t.Helper()

	rec := post(t, srv, "/absence", url.Values{
		"kind": {kind}, "starts_on": {from}, "ends_on": {to}, "note": {note},
	})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("POST /absence = %d: %s", rec.Code, rec.Body.String())
	}
	listing := get(t, srv, "/absence")
	if listing.Code != http.StatusOK {
		t.Fatalf("GET /absence = %d", listing.Code)
	}
	return listing.Body.String()
}

func TestTheLeaveFormDoublesAsACorrectionForm(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	if body := recordAbsence(t, srv, "sick", "2026-03-16", "2026-03-16", "Flu"); !strings.Contains(body, "Flu") {
		t.Fatal("the absence was not recorded")
	}

	// The edit screen shows the record in the form at the top.
	edit := get(t, srv, "/absence/1/edit")
	if edit.Code != http.StatusOK {
		t.Fatalf("GET /absence/1/edit = %d: %s", edit.Code, edit.Body.String())
	}
	for _, want := range []string{
		`action="/absence/1"`, `value="2026-03-16"`, `value="Flu"`,
	} {
		if !strings.Contains(edit.Body.String(), want) {
			t.Errorf("the correction form does not carry %s", want)
		}
	}
	// And says which row it is correcting.
	if !strings.Contains(edit.Body.String(), "is-editing") {
		t.Error("the row being corrected is not marked, so the form at the " +
			"top of the screen belongs to nothing in particular")
	}

	// Saving a longer span: the sick day that turned into three.
	save := post(t, srv, "/absence/1", url.Values{
		"kind": {"sick"}, "starts_on": {"2026-03-16"}, "ends_on": {"2026-03-18"},
		"note": {"Flu, and it kept going"},
	})
	if save.Code != http.StatusSeeOther && save.Code != http.StatusOK {
		t.Fatalf("POST /absence/1 = %d: %s", save.Code, save.Body.String())
	}

	listing := get(t, srv, "/absence").Body.String()
	if !strings.Contains(listing, "2026-03-18") {
		t.Error("the correction did not land")
	}
	if strings.Count(listing, "Flu") != 1 {
		t.Error("the correction produced a second record rather than changing the one")
	}
}

func TestACalendarCanBeReadAsDaysAwayRatherThanAsTime(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// What a national holiday feed looks like: all-day events, plus the
	// meeting that is in every real calendar and is not a day off.
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:christmas\r\nDTSTART;VALUE=DATE:20261224\r\n" +
		"DTEND;VALUE=DATE:20261226\r\nSUMMARY:Julafton\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:standup\r\nDTSTART:20261224T090000Z\r\n" +
		"DTEND:20261224T091500Z\r\nSUMMARY:Standup\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	preview := uploadTo(t, srv, "/calendar/preview", "file", "holidays.ics", ics,
		url.Values{"target": {"absence"}, "absence_kind": {"public"}})
	if preview.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", preview.Code, preview.Body.String())
	}
	body := preview.Body.String()
	// The all-day event is offered as two days; the meeting is refused by name.
	if !strings.Contains(body, "2026-12-24") || !strings.Contains(body, "2026-12-25") {
		t.Error("the preview does not show the span the all-day event covers")
	}
	if strings.Contains(body, "2026-12-26") {
		t.Error("the preview reads the calendar's exclusive end date as a day off")
	}
	if !strings.Contains(body, "not an all-day event") {
		t.Error("the fifteen-minute meeting was not refused by name")
	}
	// Nothing is written by a preview.
	if listing := get(t, srv, "/absence").Body.String(); strings.Contains(listing, "Julafton") {
		t.Fatal("the preview recorded the absence")
	}

	commit := post(t, srv, "/calendar", url.Values{
		"calendar":     {ics},
		"target":       {"absence"},
		"absence_kind": {"public"},
		"event":        {"christmas", "standup"},
	})
	if commit.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", commit.Code, commit.Body.String())
	}
	if !strings.Contains(commit.Body.String(), "Standup") {
		t.Error("the ticked meeting was dropped without saying so")
	}

	listing := get(t, srv, "/absence").Body.String()
	if !strings.Contains(listing, "Julafton") {
		t.Error("the holiday was not recorded")
	}
	if strings.Contains(listing, "Standup") {
		t.Error("a fifteen-minute meeting became a day off")
	}
}
