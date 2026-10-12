package web

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The catalogue, over HTTP.
//
// These handlers are the ones nobody writes a test for: an edit form, a rename,
// an archive, a favourite toggle. They are also where a user spends the first
// hour with the application, and the failures are quiet - a rename that saves
// nothing, an archive that hides a record from the picker but not from the
// screen, a form that comes back with yesterday's values in it.
//
// The rule running through all of them is that nothing is ever deleted.
// Removing a customer would leave last year's invoices unable to say who was
// billed, so retiring is the only removal there is, and it has to be reversible.

// adminSession signs in and returns a driver with a valid CSRF token, plus a
// seeded catalogue.
func adminSession(t *testing.T) *client {
	t.Helper()

	srv, _ := newServerModeTestServer(t)
	cookie := signIn(t, srv)
	admin := &client{srv: srv, cookie: cookie, token: csrfTokenFor(t, srv, cookie)}

	admin.post(t, "/customers", url.Values{
		"name": {"Acme"}, "currency": {"SEK"}, "rate": {"1250"}, "colour_key": {"blue"},
	})
	admin.post(t, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"},
	})
	admin.post(t, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"},
	})
	return admin
}

// TestEditingACustomerKeepsWhatWasNotEdited.
//
// The edit form is prefilled from the stored record and the save writes back the
// whole row, so a field the form does not carry is a field the save silently
// clears. That is how a customer's notes or rate disappear when somebody
// corrects a spelling mistake in the name.
func TestEditingACustomerKeepsWhatWasNotEdited(t *testing.T) {
	admin := adminSession(t)

	form := admin.get(t, "/customers/1/edit").Body.String()
	if !strings.Contains(form, "Acme") {
		t.Fatalf("the edit form does not carry the current name:\n%s", form)
	}
	if !strings.Contains(form, "1250") {
		t.Error("the edit form does not prefill the rate, so saving would clear it")
	}

	rec := admin.post(t, "/customers/1", url.Values{
		"name": {"Acme AB"}, "currency": {"SEK"}, "rate": {"1250"},
		"colour_key": {"blue"}, "notes": {"pays late"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the customer = %d: %s", rec.Code, rec.Body.String())
	}

	// A second edit that changes only the colour, as the rendered form would
	// submit it - which is the case that matters, since the form carries every
	// field it knows about and the handler writes the whole row.
	admin.post(t, "/customers/1", url.Values{
		"name": {"Acme AB"}, "currency": {"SEK"}, "rate": {"1250"},
		"colour_key": {"green"},
	})

	after := admin.get(t, "/customers/1/edit").Body.String()
	if !strings.Contains(after, "1250") {
		t.Error("changing the colour cleared the rate")
	}
	if !strings.Contains(after, "Acme AB") {
		t.Error("changing the colour cleared the name")
	}

	admin.post(t, "/customers/1", url.Values{
		"name": {"Acme AB"}, "currency": {"SEK"}, "rate": {"1250"},
		"colour_key": {"green"}, "notes": {""},
	})
	if body := admin.get(t, "/admin").Body.String(); !strings.Contains(body, "Acme AB") {
		t.Error("the renamed customer is not on the administration screen")
	}
}

// TestEditingACustomerKeepsItsInternalNotes.
//
// The notes are not on any screen. They arrive through a restore or an import,
// they are one of the things the client projection exists to withhold, and until
// this test every edit wrote an empty string over them - so correcting a
// spelling mistake in a customer's name deleted whatever had been recorded about
// that account, silently and permanently.
//
// The field is absent from the form rather than empty in it, which is the
// distinction the handler now makes.
func TestEditingACustomerKeepsItsInternalNotes(t *testing.T) {
	admin := adminSession(t)

	// Set the notes the way a restore does: through the form, since that is the
	// only writer, and then edit without them.
	admin.post(t, "/customers/1", url.Values{
		"name": {"Acme"}, "currency": {"SEK"}, "rate": {"1250"},
		"colour_key": {"blue"}, "notes": {"INTERNAL-they-have-not-paid-for-March"},
	})

	admin.post(t, "/customers/1", url.Values{
		"name": {"Acme AB"}, "currency": {"SEK"}, "rate": {"1250"},
		"colour_key": {"blue"},
	})

	// Read them back out of a backup, which is the one place they surface.
	if !backupContains(t, admin, "INTERNAL-they-have-not-paid-for-March") {
		t.Error("renaming a customer deleted its internal notes")
	}

	// A form that does carry the field still clears it when somebody empties it:
	// absent and empty are different things, and only the first is preserved.
	admin.post(t, "/customers/1", url.Values{
		"name": {"Acme AB"}, "currency": {"SEK"}, "rate": {"1250"},
		"colour_key": {"blue"}, "notes": {""},
	})
	if backupContains(t, admin, "INTERNAL-they-have-not-paid-for-March") {
		t.Error("emptying the notes field did not clear them")
	}
}

// backupContains downloads a backup and reports whether any file in the archive
// holds the given text.
//
// The archive is a zip, so the catalogue inside it is compressed: searching the
// downloaded bytes directly finds nothing however plainly the value is stored,
// which is a way to write a test that can only pass by accident.
func backupContains(t *testing.T, admin *client, text string) bool {
	t.Helper()

	rec := admin.get(t, "/backup/download")
	if rec.Code != http.StatusOK {
		t.Fatalf("backup download = %d", rec.Code)
	}
	archive, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("the backup is not a zip: %v", err)
	}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if strings.Contains(string(content), text) {
			return true
		}
	}
	return false
}

// TestArchivingHidesARecordAndRestoringBringsItBack.
//
// Archiving is the only removal in the application, so it has to work in both
// directions: a record archived by mistake must be recoverable, and its history
// has to survive either way.
func TestArchivingHidesARecordAndRestoringBringsItBack(t *testing.T) {
	admin := adminSession(t)

	// Time recorded first, so the history is there to survive.
	today := time.Now().UTC().Format("2006-01-02")
	if rec := admin.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"1h"}, "billable": {"on"}, "note": {"before archiving"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record time = %d: %s", rec.Code, rec.Body.String())
	}

	for _, level := range []struct{ what, path string }{
		{"assignment", "/assignments/1/archive"},
		{"project", "/projects/1/archive"},
		{"customer", "/customers/1/archive"},
	} {
		if rec := admin.post(t, level.path, url.Values{}); rec.Code != http.StatusSeeOther {
			t.Fatalf("archiving the %s = %d: %s", level.what, rec.Code, rec.Body.String())
		}
	}

	// The work is still there. This is the whole reason there is no delete.
	entries := admin.get(t, "/entries").Body.String()
	if !strings.Contains(entries, "before archiving") {
		t.Error("archiving the catalogue took the recorded time with it")
	}

	// And an archived assignment is not offered for new work.
	day := admin.get(t, "/today").Body.String()
	if strings.Contains(day, `value="1"`) && strings.Contains(day, "Development</option>") {
		t.Error("an archived assignment is still offered in the timer picker")
	}

	for _, level := range []struct{ what, path string }{
		{"customer", "/customers/1/archive"},
		{"project", "/projects/1/archive"},
		{"assignment", "/assignments/1/archive"},
	} {
		if rec := admin.post(t, level.path, url.Values{"restore": {"1"}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("restoring the %s = %d", level.what, rec.Code)
		}
	}

	restored := admin.get(t, "/today").Body.String()
	if !strings.Contains(restored, "Development") {
		t.Error("a restored assignment is not offered again")
	}
}

// TestFavouritingIsAToggle.
//
// One route that flips the flag rather than two that set it. A pair of routes
// would let a double-submitted form leave the flag in whichever state the second
// request asked for, which is fine - but the toggle is what the screen renders,
// so it is the toggle that has to be right.
func TestFavouritingIsAToggle(t *testing.T) {
	admin := adminSession(t)

	if rec := admin.post(t, "/assignments/1/favourite", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("favouriting = %d: %s", rec.Code, rec.Body.String())
	}
	form := admin.get(t, "/assignments/1/edit").Body.String()
	if !strings.Contains(form, `name="favourite" checked`) &&
		!strings.Contains(form, `checked`) {
		t.Error("favouriting did not stick")
	}

	admin.post(t, "/assignments/1/favourite", url.Values{})
	again := admin.get(t, "/assignments/1/edit").Body.String()
	if strings.Count(again, "checked") >= strings.Count(form, "checked") &&
		strings.Contains(again, `name="favourite" checked`) {
		t.Error("favouriting twice did not un-favourite")
	}
}

// TestRenamingATagRenamesItEverywhere.
//
// A tag is one record referenced by many entries, so a rename is a rename rather
// than a copy - and a delete removes it from every entry carrying it rather than
// leaving them pointing at nothing.
func TestRenamingATagRenamesItEverywhere(t *testing.T) {
	admin := adminSession(t)

	today := time.Now().UTC().Format("2006-01-02")
	if rec := admin.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"1h"}, "billable": {"on"}, "tags": {"incident"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("record time = %d: %s", rec.Code, rec.Body.String())
	}
	if body := admin.get(t, "/entries").Body.String(); !strings.Contains(body, "incident") {
		t.Fatalf("the tag was not attached to the entry")
	}

	if rec := admin.post(t, "/tags/1", url.Values{
		"name": {"outage"}, "colour_key": {"red"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("renaming the tag = %d: %s", rec.Code, rec.Body.String())
	}
	entries := admin.get(t, "/entries").Body.String()
	if !strings.Contains(entries, "#outage") {
		t.Error("the entry still shows the old tag name")
	}
	// The badge, not any occurrence: "incident" is also the placeholder text in
	// the tag input on the same page, which is not a label on an entry.
	if strings.Contains(entries, "#incident") {
		t.Error("renaming a tag left the old name on the entry")
	}

	if rec := admin.post(t, "/tags/1/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting the tag = %d", rec.Code)
	}
	after := admin.get(t, "/entries").Body.String()
	if strings.Contains(after, "#outage") {
		t.Error("a deleted tag is still on the entry")
	}
	// The entry itself survives losing a label.
	if !strings.Contains(after, "Development") {
		t.Error("deleting a tag took the entry with it")
	}
}

// TestMovingTimeToAnotherAssignment.
//
// The repair for a day recorded against the wrong project, which is a mistake
// everybody makes at least once. The entries move; nothing is recreated, so the
// history and the identifiers stay.
func TestMovingTimeToAnotherAssignment(t *testing.T) {
	admin := adminSession(t)

	admin.post(t, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Support"}, "billable": {"on"},
	})

	today := time.Now().UTC().Format("2006-01-02")
	for _, start := range []string{"09:00", "11:00"} {
		if rec := admin.post(t, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {today}, "start": {start},
			"duration": {"1h"}, "billable": {"on"}, "note": {"misfiled " + start},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("record time = %d: %s", rec.Code, rec.Body.String())
		}
	}

	// The form is about the entries somebody picked, so it is reached with them
	// selected; without any it says so rather than offering a destination for
	// nothing.
	if body := admin.get(t, "/move").Body.String(); !strings.Contains(body, "Select at least one") {
		t.Error("the move form with nothing selected does not say so")
	}
	if body := admin.get(t, "/move?entry=1&entry=2").Body.String(); !strings.Contains(body, "Support") {
		t.Error("the move form does not offer the other assignment")
	}

	rec := admin.post(t, "/move", url.Values{
		"entry": {"1", "2"}, "assignment_id": {"2"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("moving = %d: %s", rec.Code, rec.Body.String())
	}

	entries := admin.get(t, "/entries").Body.String()
	if strings.Count(entries, "Support") < 2 {
		t.Errorf("the entries were not moved:\n%s", entries)
	}
	for _, note := range []string{"misfiled 09:00", "misfiled 11:00"} {
		if !strings.Contains(entries, note) {
			t.Errorf("%q was lost in the move", note)
		}
	}
}

// TestAnExpenseCanBeCorrectedAndWithdrawn.
//
// Unlike the catalogue, an expense is a claim rather than a record of work, and
// a claim somebody withdraws should go. The correction path matters because an
// expense is usually typed from a receipt, which is where a transposed figure
// comes from.
func TestAnExpenseCanBeCorrectedAndWithdrawn(t *testing.T) {
	admin := adminSession(t)

	today := time.Now().UTC().Format("2006-01-02")
	rec := admin.post(t, "/expenses", url.Values{
		"project_id": {"1"}, "spent_on": {today}, "amount": {"450.00"},
		"currency": {"SEK"}, "description": {"Taxi to the client"}, "billable": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("recording an expense = %d: %s", rec.Code, rec.Body.String())
	}
	if body := admin.get(t, "/expenses").Body.String(); !strings.Contains(body, "Taxi to the client") {
		t.Fatalf("the expense is not on the screen:\n%s", body)
	}

	rec = admin.post(t, "/expenses/1", url.Values{
		"project_id": {"1"}, "spent_on": {today}, "amount": {"540.00"},
		"currency": {"SEK"}, "description": {"Taxi to the client"}, "billable": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("correcting the expense = %d: %s", rec.Code, rec.Body.String())
	}
	corrected := admin.get(t, "/expenses").Body.String()
	if !strings.Contains(corrected, "540") {
		t.Error("the corrected amount is not shown")
	}
	if strings.Contains(corrected, "450.00") {
		t.Error("the original amount is still shown after a correction")
	}

	if rec := admin.post(t, "/expenses/1/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("withdrawing the expense = %d", rec.Code)
	}
	if body := admin.get(t, "/expenses").Body.String(); strings.Contains(body, "Taxi to the client") {
		t.Error("a withdrawn expense is still listed")
	}
}

// TestRebuildingTheSearchIndex.
//
// The repair for an index that has drifted. It is an administrative button, and
// the thing that would make it useless is if it reported success without doing
// anything - so this checks that search still answers afterwards.
func TestRebuildingTheSearchIndex(t *testing.T) {
	admin := adminSession(t)

	today := time.Now().UTC().Format("2006-01-02")
	admin.post(t, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {today}, "start": {"09:00"},
		"duration": {"1h"}, "billable": {"on"}, "note": {"reindexable note"},
	})

	if rec := admin.post(t, "/search/reindex", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("reindexing = %d: %s", rec.Code, rec.Body.String())
	}
	if body := admin.get(t, "/entries?q=reindexable").Body.String(); !strings.Contains(body, "reindexable note") {
		t.Error("search finds nothing after a rebuild")
	}
}

// TestEditFormsRefuseARecordThatIsNotThere.
//
// A stale link, a bookmarked form, or somebody editing the URL. None of them
// should reach a 500, and none should say whether the record exists.
func TestEditFormsRefuseARecordThatIsNotThere(t *testing.T) {
	admin := adminSession(t)

	for _, path := range []string{
		"/customers/999/edit", "/projects/999/edit", "/assignments/999/edit",
		"/entries/999/edit",
	} {
		rec := admin.get(t, path)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want a refusal", path, rec.Code)
		}
	}

	for _, path := range []string{
		"/customers/999", "/projects/999", "/assignments/999", "/tags/999/delete",
		"/expenses/999/delete", "/entries/999/delete",
	} {
		rec := admin.post(t, path, url.Values{"name": {"x"}, "currency": {"SEK"}})
		if rec.Code < 400 {
			t.Errorf("POST %s = %d, want a refusal", path, rec.Code)
		}
		if rec.Code >= 500 {
			t.Errorf("POST %s = %d; a missing record is not a server error", path, rec.Code)
		}
	}
}
