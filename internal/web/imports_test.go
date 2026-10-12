package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The two import screens, at the HTTP level.
//
// Both are two-pass: parse, show what would happen, then write only what the
// person confirmed. The service proves the parsing. These are about the parts
// only a request exercises - a multipart upload, a missing file, a size limit,
// and the selection carried back from the preview - because an import that
// wrote something other than what the preview showed would be discovered in an
// invoice.

// uploadTo posts a single file as multipart/form-data, with any extra fields
// alongside it, the way a browser submits an upload form.
func uploadTo(t *testing.T, srv *Server, path, field, filename, content string,
	fields url.Values) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, value := range values {
			if err := form.WriteField(key, value); err != nil {
				t.Fatalf("write field %s: %v", key, err)
			}
		}
	}
	if field != "" {
		part, err := form.CreateFormFile(field, filename)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

const timesheetCSV = "date,hours,customer,project,assignment,note,billable\n" +
	"2026-03-16,1.5,Acme,Migration,Development,Refactoring,yes\n" +
	"2026-03-17,2h30,Acme,Migration,Development,More work,yes\n"

// TestTheCSVImportPreviewWritesNothing.
//
// The whole reason for two passes. If the preview wrote rows, somebody
// reviewing what an import would do would already have done it - and the
// screen's own wording would be a lie.
func TestTheCSVImportPreviewWritesNothing(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	if rec := get(t, srv, "/import"); rec.Code != http.StatusOK {
		t.Fatalf("GET /import = %d", rec.Code)
	}

	rec := uploadTo(t, srv, "/import/preview", "file", "hours.csv", timesheetCSV, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	// The preview lists each row by line number, date, length and where it
	// would go - not the note, which is not what somebody checks.
	body := rec.Body.String()
	for _, shown := range []string{"1h 30m", "2h 30m", "Development", "Acme"} {
		if !strings.Contains(body, shown) {
			t.Errorf("the preview does not show %q", shown)
		}
	}
	if rows := strings.Count(body, `class="entry-row`); rows != 2 {
		t.Errorf("the preview lists %d rows, want the file's 2", rows)
	}
	if strings.Contains(body, "badge-flagged") {
		t.Errorf("the preview reports a problem with a file that has none:\n%s", body)
	}

	if got := totalOn(t, srv, "2026-03-16"); got != noTime {
		t.Errorf("the preview wrote time: 2026-03-16 totals %q", got)
	}

	// Committing the same file writes it.
	if rec := uploadTo(t, srv, "/import", "file", "hours.csv", timesheetCSV, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("commit = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-16"); got != "1h 30m" {
		t.Errorf("2026-03-16 totals %q after the import, want 1h 30m", got)
	}
	if got := totalOn(t, srv, "2026-03-17"); got != "2h 30m" {
		t.Errorf("2026-03-17 totals %q after the import, want 2h 30m", got)
	}
}

// TestAnImportWithNoFileIsRefusedPlainly.
//
// A form submitted with nothing chosen, which is an ordinary mistake. It must
// say so rather than return a server error, and must not be read as an import
// of nothing that quietly succeeds.
func TestAnImportWithNoFileIsRefusedPlainly(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	for _, path := range []string{"/import/preview", "/import", "/calendar/preview"} {
		t.Run(path, func(t *testing.T) {
			rec := uploadTo(t, srv, path, "", "", "", nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s with no file = %d, want 400\n%s",
					path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "No file") {
				t.Errorf("POST %s does not say a file is missing: %q", path, rec.Body.String())
			}
		})
	}

	// A body that is not multipart at all is the same kind of mistake.
	for _, path := range []string{"/import/preview", "/calendar/preview"} {
		rec := post(t, srv, path, url.Values{"file": {"not a file"}})
		if rec.Code == http.StatusInternalServerError {
			t.Errorf("POST %s with a URL-encoded body = 500, want a refusal", path)
		}
	}
}

// TestImportingCanCreateTheCatalogueItNeeds.
//
// The flag on the form. Off, a row naming an unknown project is a problem to
// look at; on, it is a project to create. Importing a year of history from
// another system is the case this exists for, and the flag has to reach the
// service from both passes or the preview would describe a different import
// from the one that runs.
func TestImportingCanCreateTheCatalogueItNeeds(t *testing.T) {
	srv, _ := newTestServer(t)

	const csv = "date,hours,customer,project,assignment\n" +
		"2026-03-16,1.5,Globex,Rollout,Support\n"

	// Nothing in the catalogue, and the flag off: the row is a problem.
	strict := uploadTo(t, srv, "/import/preview", "file", "hours.csv", csv, nil)
	if strict.Code != http.StatusOK {
		t.Fatalf("strict preview = %d: %s", strict.Code, strict.Body.String())
	}
	if !strings.Contains(strict.Body.String(), "Globex") {
		t.Error("the preview does not name the row it cannot place")
	}

	// With the flag, the same file is importable.
	lenient := uploadTo(t, srv, "/import/preview", "file", "hours.csv", csv,
		url.Values{"create_missing": {"on"}})
	if lenient.Code != http.StatusOK {
		t.Fatalf("lenient preview = %d: %s", lenient.Code, lenient.Body.String())
	}

	if rec := uploadTo(t, srv, "/import", "file", "hours.csv", csv,
		url.Values{"create_missing": {"on"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("commit = %d: %s", rec.Code, rec.Body.String())
	}
	if got := totalOn(t, srv, "2026-03-16"); got != "1h 30m" {
		t.Errorf("2026-03-16 totals %q after importing with creation allowed", got)
	}
	if !strings.Contains(get(t, srv, "/admin").Body.String(), "Globex") {
		t.Error("the import did not create the customer it was allowed to create")
	}
}

// TestACalendarImportWritesOnlyWhatWasTicked.
//
// A calendar is not a timesheet: it holds cancelled meetings, meetings somebody
// declined, and blocks put in to protect an afternoon. The preview separates
// those out; the commit must write only the events the person ticked, and
// nothing it merely listed.
func TestACalendarImportWritesOnlyWhatWasTicked(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	if rec := get(t, srv, "/calendar"); rec.Code != http.StatusOK {
		t.Fatalf("GET /calendar = %d", rec.Code)
	}

	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:kept\r\nDTSTART:20260316T080000Z\r\nDTEND:20260316T093000Z\r\n" +
		"SUMMARY:Planning\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:skipped\r\nDTSTART:20260316T100000Z\r\nDTEND:20260316T110000Z\r\n" +
		"SUMMARY:Standup\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	preview := uploadTo(t, srv, "/calendar/preview", "file", "work.ics", ics,
		url.Values{"default_assignment": {"1"}})
	if preview.Code != http.StatusOK {
		t.Fatalf("calendar preview = %d: %s", preview.Code, preview.Body.String())
	}
	for _, shown := range []string{"Planning", "Standup"} {
		if !strings.Contains(preview.Body.String(), shown) {
			t.Errorf("the preview does not list the event %q", shown)
		}
	}
	if got := totalOn(t, srv, "2026-03-16"); got != noTime {
		t.Errorf("the calendar preview wrote time: the day totals %q", got)
	}

	// The file travels back with the form, so committing does not need it
	// uploaded twice. Tick one event only.
	commit := post(t, srv, "/calendar", url.Values{
		"calendar":           {ics},
		"event":              {"kept"},
		"assignment_kept":    {"1"},
		"assignment_skipped": {"1"},
		"note":               {"From the calendar"},
	})
	if commit.Code != http.StatusOK {
		t.Fatalf("calendar import = %d: %s", commit.Code, commit.Body.String())
	}

	if got := totalOn(t, srv, "2026-03-16"); got != "1h 30m" {
		t.Errorf("the day totals %q after importing one 90-minute event, want 1h 30m", got)
	}

	// Ticking nothing is a mistake worth naming rather than an import of
	// nothing that reports success.
	nothing := post(t, srv, "/calendar", url.Values{"calendar": {ics}})
	if nothing.Code == http.StatusOK {
		t.Error("a calendar import with nothing ticked reported success")
	}
	if !strings.Contains(nothing.Body.String(), "nothing was selected") {
		t.Errorf("the refusal does not say nothing was selected: %q", nothing.Body.String())
	}
}

// TestAnUploadTooLargeIsRefusedWithoutBeingRead.
//
// The limit exists so that a request cannot make the process hold an arbitrary
// amount of memory. It has to be enforced before the body is read, which is why
// it is asserted on the response rather than on what was imported.
func TestAnUploadTooLargeIsRefusedWithoutBeingRead(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// A header plus a great many rows, past the cap.
	var huge strings.Builder
	huge.WriteString("date,hours,assignment\n")
	for huge.Len() <= maxUploadBytes {
		huge.WriteString("2026-03-16,1,Development\n")
	}

	for _, path := range []string{"/import/preview", "/import", "/calendar/preview"} {
		t.Run(path, func(t *testing.T) {
			rec := uploadTo(t, srv, path, "file", "huge.csv", huge.String(), nil)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("POST %s with an oversized upload = %d, want 413", path, rec.Code)
			}
		})
	}

	// Nothing was written by any of them.
	if got := totalOn(t, srv, "2026-03-16"); got != noTime {
		t.Errorf("an oversized upload wrote time: the day totals %q", got)
	}
}
