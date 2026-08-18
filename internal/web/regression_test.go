package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// Regression tests.
//
// Every test here corresponds to a defect that actually reached a running
// build. They are gathered in one file, each named for its symptom rather than
// for the code it touches, because the point of a regression test is to fail if
// the same mistake is made again - not to document a module.
//
// A bug without a test here is a bug that will come back.

// A completed customer form was rejected with "customer name is required".
//
// fetch(FormData) sends multipart/form-data. r.ParseForm does not parse a
// multipart body, but it *does* set r.Form - so the later r.FormValue never
// falls back to the multipart parser and every field arrived empty. The handler
// then reported the form as blank when the user had filled it in completely.
func TestRegressionMultipartFormFieldsAreNotLost(t *testing.T) {
	srv, _ := newTestServer(t)

	var body strings.Builder
	const boundary = "----regression"
	for _, field := range []struct{ name, value string }{
		{"name", "MCF"},
		{"currency", "SEK"},
		{"colour_key", "blue"},
	} {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(`Content-Disposition: form-data; name="` + field.name + `"` + "\r\n\r\n")
		body.WriteString(field.value + "\r\n")
	}
	body.WriteString("--" + boundary + "--\r\n")

	req := httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a completed multipart form was rejected: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(get(t, srv, "/admin").Body.String(), "MCF") {
		t.Error("the customer was not created")
	}
}

// Every screen with entries returned 500 after the templates were translated.
//
// entry-row is rendered inside a range and receives a dict, so $ no longer
// resolved to the page data and {{$.T ...}} failed at execution time. The
// failure was invisible in tests because the test logger discards output, and
// the only symptom was a 500 on any page that had at least one entry.
func TestRegressionPagesWithEntriesRender(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}})

	// The bug only appeared once there was something to render in the row
	// fragment, so an empty-page smoke test would not have caught it.
	for _, path := range []string{"/today", "/entries", "/week"} {
		rec := get(t, srv, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s with entries = %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "<table") &&
			!strings.Contains(rec.Body.String(), "week-grid") {
			t.Errorf("GET %s rendered no content", path)
		}
	}
}

// The day view went blank the moment scoped queries landed.
//
// Day and Week called the store directly with a zero Scope, which renders as
// "match nothing" - by design, so a forgotten scope fails safe. The lesson is
// the test, not the fix: any screen that lists records must be exercised with
// records present.
func TestRegressionScopedQueriesReturnTheUsersOwnData(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"Findable"}, "billable": {"on"}})
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "duration": {"1h"}, "billable": {"on"},
	})

	for _, path := range []string{"/today", "/week", "/entries"} {
		body := get(t, srv, path).Body.String()
		if !strings.Contains(body, "Findable") {
			t.Errorf("GET %s does not show the user's own entry", path)
		}
	}
}

// PDF and DOCX export returned 501 long after they were implemented, because
// the handler's switch still listed them as unimplemented.
func TestRegressionAllFourExportFormatsWork(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct{ format, prefix, contentType string }{
		{"pdf", "%PDF-", "application/pdf"},
		{"docx", "PK\x03\x04", "application/vnd.openxmlformats"},
		{"csv", "\xef\xbb\xbf", "text/csv"},
		{"json", "{", "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			rec := get(t, srv, "/export/"+tc.format)
			if rec.Code != http.StatusOK {
				t.Fatalf("export = %d", rec.Code)
			}
			if !strings.HasPrefix(rec.Body.String(), tc.prefix) {
				t.Errorf("the %s export does not begin as that format", tc.format)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), tc.contentType) {
				t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// SECURITY.md claimed the sniffed content type must agree with the file
// extension, and no such check existed: a shell script named .png was stored.
// The claim is now true, and this is what keeps it true.
func TestRegressionExtensionMustMatchContent(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/expenses", url.Values{
		"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"description": {"Taxi"}, "amount": {"100"}, "billable": {"on"},
	})

	upload := func(filename, content string) int {
		var body strings.Builder
		const boundary = "----upload"
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(`Content-Disposition: form-data; name="file"; filename="` +
			filename + `"` + "\r\n")
		body.WriteString("Content-Type: application/octet-stream\r\n\r\n")
		body.WriteString(content + "\r\n")
		body.WriteString("--" + boundary + "--\r\n")

		req := httptest.NewRequest(http.MethodPost, "/attachments/expense/1",
			strings.NewReader(body.String()))
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := upload("innocent.png", "#!/bin/sh\necho hello\n"); code == http.StatusSeeOther {
		t.Error("a shell script named .png was accepted")
	}
	if code := upload("notes.txt", "these are some notes"); code != http.StatusSeeOther {
		t.Errorf("an honest text file was refused: %d", code)
	}
}

// The macOS and Windows builds broke because a symbol used by shared code was
// defined only in a _linux.go file. No test can catch that - only a
// cross-compile can - so `make build-check` exists and runs in `make check`.
// This test asserts the guard is still wired in, since a Makefile edit could
// silently remove it.
func TestRegressionCrossCompileCheckIsWiredIn(t *testing.T) {
	makefile, err := readRepoFile("Makefile")
	if err != nil {
		t.Skipf("cannot read the Makefile: %v", err)
	}
	if !strings.Contains(makefile, "build-check:") {
		t.Error("the cross-compile target is gone")
	}
	// It has to be part of `check`, not merely present.
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, "check:") {
			if !strings.Contains(line, "build-check") {
				t.Errorf("`make check` no longer cross-compiles: %s", line)
			}
			return
		}
	}
	t.Error("no check target found")
}

// readRepoFile reads a file from the repository root, which is two levels up
// from this package.
func readRepoFile(name string) (string, error) {
	content, err := os.ReadFile("../../" + name)
	return string(content), err
}
