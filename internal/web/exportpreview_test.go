package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The screen between a selection and its download.
//
// An export is the basis of an invoice, and the screen it is taken from shows
// fifty rows at a time while the download covers the whole filter. What matters
// at this layer is that the preview and the download carry the same filter: a
// preview describing a different document from the one that arrives would be
// worse than none, because its figure is the one somebody quotes.

// TestTheEntriesScreenOffersAPreviewBeforeTheDownload.
func TestTheEntriesScreenOffersAPreviewBeforeTheDownload(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"4h"},
	})

	entries := get(t, srv, "/entries")
	if entries.Code != http.StatusOK {
		t.Fatalf("GET /entries = %d", entries.Code)
	}
	if !strings.Contains(entries.Body.String(), "/export/preview") {
		t.Error("the entries screen offers no preview of what a download would hold")
	}
	// The download links are still there: the preview is an extra step, not a
	// gate in front of a file somebody already knows they want.
	for _, format := range []string{"/export/pdf", "/export/csv"} {
		if !strings.Contains(entries.Body.String(), format) {
			t.Errorf("the entries screen no longer links to %s", format)
		}
	}
}

// TestThePreviewAndTheDownloadCarryTheSameFilter.
//
// Both links are built from one query, so a preview taken with a search box
// filled in describes the download that search box produces. If they came
// apart, somebody would check one document and send another.
func TestThePreviewAndTheDownloadCarryTheSameFilter(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"},
		"duration": {"4h"}, "note": {"Migration planning"},
	})

	const query = "?from=2026-03-01&to=2026-03-31&q=planning"
	listing := get(t, srv, "/entries"+query)
	if listing.Code != http.StatusOK {
		t.Fatalf("GET the filtered listing = %d", listing.Code)
	}

	previewHref := hrefContaining(t, listing.Body.String(), "/export/preview")
	csvHref := hrefContaining(t, listing.Body.String(), "/export/csv")
	if previewHref == "" || csvHref == "" {
		t.Fatalf("the filtered listing does not carry both links:\n%s",
			truncate(listing.Body.String()))
	}

	previewQuery := queryOf(previewHref)
	csvQuery := queryOf(csvHref)
	if previewQuery != csvQuery {
		t.Errorf("the preview carries %q and the CSV download carries %q; a "+
			"preview of a different selection is worse than none",
			previewQuery, csvQuery)
	}
	// And the filter actually travelled, rather than both being empty.
	if !strings.Contains(previewQuery, "planning") {
		t.Errorf("the search text did not reach the preview link: %q", previewQuery)
	}
}

// TestThePreviewSaysWhatTheDownloadHolds.
//
// The count, the hours, the money and the breakdown an invoice is written
// from - over the whole filter rather than the page.
func TestThePreviewSaysWhatTheDownloadHolds(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	// More rows than a page holds, so the page cap would show if it leaked.
	for day := 1; day <= 20; day++ {
		if rec := post(t, srv, "/entries", url.Values{
			"assignment_id": {"1"},
			"date":          {"2026-03-" + twoDigits(day)},
			"start":         {"09:00"}, "duration": {"2h"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("record day %d = %d", day, rec.Code)
		}
	}

	preview := get(t, srv, "/export/preview?from=2026-03-01&to=2026-03-31")
	if preview.Code != http.StatusOK {
		t.Fatalf("GET the preview = %d: %s", preview.Code, truncate(preview.Body.String()))
	}
	body := preview.Body.String()

	if !strings.Contains(body, "20 entries") {
		t.Errorf("the preview does not say it holds twenty entries:\n%s", truncate(body))
	}
	if !strings.Contains(body, "40h 00m") {
		t.Error("the preview does not show the hours the download holds")
	}
	// The customer the work is for, and the project under it.
	if !strings.Contains(body, "Acme") {
		t.Error("the preview does not name the customer")
	}
	if !strings.Contains(body, "Migration") {
		t.Error("the preview does not name the project")
	}
	// And a way to get each format.
	for _, format := range []string{"/export/pdf", "/export/docx", "/export/csv"} {
		if !strings.Contains(body, format) {
			t.Errorf("the preview offers no %s download", format)
		}
	}
}

// TestThePreviewNamesWhatTheDocumentLeavesOut.
//
// A flagged entry is in the selection and not in the figure. Saying so is the
// difference between a total somebody can reconcile and one they cannot.
func TestThePreviewNamesWhatTheDocumentLeavesOutOnScreen(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-16"}, "start": {"09:00"}, "duration": {"4h"},
	})
	// Not billable, which is in the document as a line and contributes nothing.
	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-17"}, "start": {"09:00"}, "duration": {"2h"},
	})

	body := get(t, srv, "/export/preview?from=2026-03-01&to=2026-03-31").Body.String()
	if !strings.Contains(body, "leaves out") {
		t.Errorf("the preview has no section for what the document omits:\n%s",
			truncate(body))
	}
	if !strings.Contains(body, "not billable") {
		t.Error("the preview does not say which rows are not billable")
	}
}

// TestAPreviewOfAnEmptySelection.
//
// A filter matching nothing is a question somebody asked. The screen says so
// rather than offering a download of an empty document.
func TestAPreviewOfAnEmptySelection(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	preview := get(t, srv, "/export/preview?from=2027-01-01&to=2027-01-31")
	if preview.Code != http.StatusOK {
		t.Fatalf("GET the preview of an empty range = %d", preview.Code)
	}
	body := preview.Body.String()
	if !strings.Contains(body, "nothing to download") {
		t.Errorf("the preview of an empty selection does not say so:\n%s", truncate(body))
	}
	if strings.Contains(body, "/export/pdf") {
		t.Error("the preview of an empty selection offers a download anyway")
	}
}

// hrefContaining returns the first href holding the given fragment.
func hrefContaining(t *testing.T, body, fragment string) string {
	t.Helper()
	for _, chunk := range strings.Split(body, `href="`)[1:] {
		end := strings.Index(chunk, `"`)
		if end < 0 {
			continue
		}
		href := chunk[:end]
		if strings.Contains(href, fragment) {
			return href
		}
	}
	return ""
}

// queryOf is the query string of a link, with its path dropped.
func queryOf(href string) string {
	if cut := strings.IndexByte(href, '?'); cut >= 0 {
		return href[cut+1:]
	}
	return ""
}

// twoDigits formats a day of the month for a date string.
func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
