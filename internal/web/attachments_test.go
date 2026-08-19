package web

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// createEntryForAttachments seeds a catalogue and one entry to attach to, and
// returns its id.
func createEntryForAttachments(t *testing.T, srv *Server) int64 {
	t.Helper()
	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"}})
	if rec := post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "duration": {"1h"}, "billable": {"on"},
	}); rec.Code >= 400 {
		t.Fatalf("create entry = %d\n%s", rec.Code, rec.Body.String())
	}
	return 1
}

// upload posts a file to an owner and returns the recorder.
func upload(t *testing.T, srv *Server, ownerType string, ownerID int64, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/attachments/"+ownerType+"/"+itoa(ownerID), &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// onePixelPNG is the smallest real PNG, so the store's sniff has something
// genuine to recognise.
var onePixelPNG = mustBase64(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func mustBase64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return data
}

// TestPreviewHeadersAreWhatMakeItSafe.
//
// This route is the only one in the application that serves stored bytes
// inline, and it exists because SVG previews were wanted. Every header on the
// response is load-bearing: the policy is what stops a hostile SVG doing
// anything if it is opened directly rather than through the <img> the page
// uses. See docs/adr/0031-attachment-previews.md.
func TestPreviewHeadersAreWhatMakeItSafe(t *testing.T) {
	srv, _ := newTestServer(t)
	entryID := createEntryForAttachments(t, srv)

	hostile := []byte(`<?xml version="1.0"?>` +
		`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">` +
		`<script>alert(1)</script></svg>`)
	if rec := upload(t, srv, "time_entry", entryID, "diagram.svg", hostile); rec.Code >= 400 {
		t.Fatalf("upload = %d, want a redirect or 2xx\n%s", rec.Code, rec.Body.String())
	}

	rec := get(t, srv, "/attachments/1/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200\n%s", rec.Code, rec.Body.String())
	}

	policy := rec.Header().Get("Content-Security-Policy")
	for _, required := range []string{"default-src 'none'", "sandbox"} {
		if !strings.Contains(policy, required) {
			t.Errorf("the response policy is missing %q; it reads %q", required, policy)
		}
	}
	// sandbox with any token would re-enable something. It must stand alone.
	if strings.Contains(policy, "allow-scripts") || strings.Contains(policy, "allow-same-origin") {
		t.Errorf("the sandbox has been given a token that undoes it: %q", policy)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q; the type must be the one determined at upload", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Errorf("Content-Disposition = %q, want inline", got)
	}
	// The bytes are served as stored. The safety of this path is in the headers
	// and the element, never in editing the file.
	if !bytes.Equal(rec.Body.Bytes(), hostile) {
		t.Error("the SVG was altered on the way out")
	}
}

// TestDownloadStaysAnAttachment: adding an inline route must not have loosened
// the one that was already there.
func TestDownloadStaysAnAttachment(t *testing.T) {
	srv, _ := newTestServer(t)
	entryID := createEntryForAttachments(t, srv)

	if rec := upload(t, srv, "time_entry", entryID, "diagram.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); rec.Code >= 400 {
		t.Fatalf("upload = %d\n%s", rec.Code, rec.Body.String())
	}

	rec := get(t, srv, "/attachments/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment; an SVG served as a "+
			"document is a stored cross-site scripting vector", got)
	}
}

// TestPreviewsAppearOnTheEntryScreen. There was no screen listing attachments
// at all, which left the download and delete routes unreachable.
func TestPreviewsAppearOnTheEntryScreen(t *testing.T) {
	srv, _ := newTestServer(t)
	entryID := createEntryForAttachments(t, srv)

	if rec := upload(t, srv, "time_entry", entryID, "shot.png", onePixelPNG); rec.Code >= 400 {
		t.Fatalf("upload = %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := upload(t, srv, "time_entry", entryID, "notes.txt",
		[]byte("a receipt for the taxi\n")); rec.Code >= 400 {
		t.Fatalf("upload = %d\n%s", rec.Code, rec.Body.String())
	}

	rec := get(t, srv, "/entries/"+itoa(entryID)+"/edit")
	if rec.Code != http.StatusOK {
		t.Fatalf("edit screen = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		`src="/attachments/1/preview"`, // the image
		`shot.png`,
		`href="/attachments/1"`,  // the download link, previously unreachable
		`/attachments/1/delete`,  // and the delete route
		`a receipt for the taxi`, // the text preview, rendered into the page
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the entry screen does not contain %q", want)
		}
	}
}

// TestTextPreviewIsNotServedAsAResource: text is escaped into the page, so
// fetching it from the preview route would be a second path to the same bytes
// with none of the page's escaping.
func TestTextPreviewIsNotServedAsAResource(t *testing.T) {
	srv, _ := newTestServer(t)
	entryID := createEntryForAttachments(t, srv)

	if rec := upload(t, srv, "time_entry", entryID, "notes.txt", []byte("plain")); rec.Code >= 400 {
		t.Fatalf("upload = %d", rec.Code)
	}
	if rec := get(t, srv, "/attachments/1/preview"); rec.Code != http.StatusNotFound {
		t.Errorf("preview of a text file = %d, want 404", rec.Code)
	}
}

// TestUnpreviewableFileStillDownloads: a preview is a convenience, never a
// precondition for reaching the bytes.
func TestUnpreviewableFileStillDownloads(t *testing.T) {
	srv, _ := newTestServer(t)
	entryID := createEntryForAttachments(t, srv)

	// A zip that is not a Word document.
	zipBytes := []byte("PK\x03\x04" + strings.Repeat("\x00", 200))
	if rec := upload(t, srv, "time_entry", entryID, "bundle.zip", zipBytes); rec.Code >= 400 {
		t.Fatalf("upload = %d\n%s", rec.Code, rec.Body.String())
	}

	if rec := get(t, srv, "/attachments/1/preview"); rec.Code != http.StatusNotFound {
		t.Errorf("preview = %d, want 404 for a file with no preview", rec.Code)
	}
	if rec := get(t, srv, "/attachments/1"); rec.Code != http.StatusOK {
		t.Errorf("download = %d, want 200: the file is still there", rec.Code)
	}
}
