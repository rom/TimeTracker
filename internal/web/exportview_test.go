package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Reading the document a download would produce, without downloading it.
//
// The preview screen says what the file will contain - the rows, the hours, the
// amounts per customer. It does not say what the file will look like: a PDF has
// a title, a layout and a subtotal per group, and a wrong one of those is
// noticed after it has been sent. So each format can be opened in the browser
// first, served inline rather than as a download.

// TestThePreviewOffersTheDocumentItself.
func TestThePreviewOffersTheDocumentItself(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"},
		"duration": {"4h"}, "note": {"Migration planning"},
	})

	const query = "?from=2026-03-01&to=2026-03-31&q=planning"
	body := get(t, srv, "/export/preview"+query).Body.String()

	viewHref := hrefContaining(t, body, "/export/pdf/view")
	if viewHref == "" {
		t.Fatalf("the preview offers no way to look at the document:\n%s", truncate(body))
	}
	// The same filter as the download beside it, or somebody checks one
	// document and sends another.
	downloadHref := hrefContaining(t, body, `/export/pdf?`)
	if downloadHref == "" {
		t.Fatal("the preview no longer links to the PDF download")
	}
	if queryOf(viewHref) != queryOf(downloadHref) {
		t.Errorf("the view carries %q and the download carries %q",
			queryOf(viewHref), queryOf(downloadHref))
	}
	if !strings.Contains(queryOf(viewHref), "planning") {
		t.Errorf("the filter did not reach the view link: %q", queryOf(viewHref))
	}
}

// TestEveryFormatCanBeLookedAtRatherThanSaved.
//
// Served inline, with the headers that make a browser display it and nothing
// else: a PDF as a PDF, and the text formats as text, because a browser handed
// text/csv saves it instead of showing it - which is the behaviour this route
// exists to avoid.
func TestEveryFormatCanBeLookedAtRatherThanSaved(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"},
		"duration": {"4h"}, "note": {"Migration planning"},
	})

	for _, format := range []struct {
		name        string
		contentType string
		holds       string
	}{
		{"pdf", "application/pdf", "%PDF"},
		// A DOCX is a zip, which no browser displays, so it arrives as the text
		// it holds rather than as the file.
		{"docx", "text/plain", "Migration planning"},
		{"csv", "text/plain", "Migration planning"},
		{"json", "text/plain", "Migration planning"},
		{"md", "text/plain", "Migration planning"},
	} {
		t.Run(format.name, func(t *testing.T) {
			rec := get(t, srv, "/export/"+format.name+"/view?from=2026-03-01&to=2026-03-31")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET the %s document = %d: %s", format.name, rec.Code,
					truncate(rec.Body.String()))
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, format.contentType) {
				t.Errorf("the %s document is served as %q, want %s",
					format.name, got, format.contentType)
			}
			// Displayed, not saved - and named for what it holds. A converted
			// DOCX is text, and a browser told to save a .docx full of plain
			// text writes a file no word processor opens.
			disposition := rec.Header().Get("Content-Disposition")
			if !strings.HasPrefix(disposition, "inline") {
				t.Errorf("the %s document is served as %q, which a browser saves "+
					"rather than shows", format.name, disposition)
			}
			wantExt := "." + format.name
			if format.name == "docx" {
				wantExt = ".txt"
			}
			if !strings.Contains(disposition, wantExt+`"`) {
				t.Errorf("the %s document is named %q, want it to end in %s",
					format.name, disposition, wantExt)
			}
			// Nothing loads and nothing runs, as with an attachment preview.
			if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
				t.Errorf("the %s document is served without a sandbox: %q", format.name, csp)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("the %s document is served with X-Content-Type-Options %q",
					format.name, got)
			}
			// Live data and somebody's billing: not cached.
			if cache := rec.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
				t.Errorf("the %s document is cacheable: %q", format.name, cache)
			}
			if !strings.Contains(rec.Body.String(), format.holds) {
				t.Errorf("the %s document does not hold %q:\n%s", format.name,
					format.holds, truncate(rec.Body.String()))
			}
		})
	}
}

// TestTheDownloadIsStillADownload.
//
// The inline route is beside the download rather than instead of it: a file
// somebody already knows they want must still arrive as a file.
func TestTheDownloadIsStillADownload(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"4h"},
	})

	for _, format := range []string{"pdf", "docx", "csv", "json", "md"} {
		rec := get(t, srv, "/export/"+format+"?from=2026-03-01&to=2026-03-31")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET the %s download = %d", format, rec.Code)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
			t.Errorf("the %s download is served as %q", format, got)
		}
	}
}

// TestLookingAtSomethingThatIsNotAFormat.
func TestLookingAtSomethingThatIsNotAFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	rec := get(t, srv, "/export/xlsx/view")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET a view of an unknown format = %d, want 400: %s",
			rec.Code, truncate(rec.Body.String()))
	}
}

// TestADocumentTooLargeToReadIsRefused.
//
// The inline route holds the whole document in memory so that a failure is an
// error page rather than half a file under a 200 - which means it needs a bound
// of its own, lower than the download's. Refused with the figure and a way
// forward, rather than attempted.
func TestADocumentTooLargeToReadIsRefused(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	for day := 1; day <= 5; day++ {
		post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"}, "date": {"2026-03-" + twoDigits(day)},
			"start": {"09:00"}, "duration": {"1h"},
		})
	}

	// The real bound is far above anything a test should seed, so it is lowered
	// for the duration rather than reached.
	restore := maxViewedExportRows
	setMaxViewedExportRows(2)
	t.Cleanup(func() { setMaxViewedExportRows(restore) })

	for _, format := range []string{"pdf", "csv"} {
		rec := get(t, srv, "/export/"+format+"/view?from=2026-03-01&to=2026-03-31")
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("GET the %s document = %d, want 413 past the limit: %s",
				format, rec.Code, truncate(rec.Body.String()))
			continue
		}
		// And it says what to do instead, which is the route that streams.
		if !strings.Contains(strings.ToLower(rec.Body.String()), "download") {
			t.Errorf("the %s refusal does not point at the download: %s",
				format, truncate(rec.Body.String()))
		}
	}

	// The download itself is unaffected: its own bound is far higher, and a
	// screen's limit must not become a file's.
	if rec := get(t, srv, "/export/csv?from=2026-03-01&to=2026-03-31"); rec.Code != http.StatusOK {
		t.Errorf("the CSV download = %d; the inline bound reached the download", rec.Code)
	}
}
