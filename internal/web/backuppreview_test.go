package web

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/i18n"
)

// Looking inside an archive on disk, through the screen.
//
// What is in a backup is the service's question and tested there. What only a
// request exercises is the rest: that the listing links to the preview, that
// the preview renders the figures rather than a template error, and that it
// says so in both languages - a screen added after the i18n sweep's path list
// was written gets no coverage from it, which is how three keys that did not
// exist reached the submission review.

// previewOfALiveBackup writes a backup, then returns the preview's URL and its
// body.
func previewOfALiveBackup(t *testing.T, srv *Server) (string, string) {
	t.Helper()

	// Something in it worth counting: a customer, a project, an assignment, a
	// day's work and a cost.
	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"},
		"billable": {"on"}, "currency": {"SEK"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"Development"},
		"billable": {"on"}})
	post(t, srv, "/entries", url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
		"start": {"09:00"}, "end": {"17:00"}})
	post(t, srv, "/expenses", url.Values{"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"category": {"travel"}, "description": {"Taxi"}, "amount": {"100"},
		"billable": {"on"}})

	if rec := post(t, srv, "/backup/create", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create backup = %d: %s", rec.Code, rec.Body.String())
	}

	// The link the screen offers, rather than one this test composes: a preview
	// reachable only by a URL nobody is given is not reachable.
	listing := get(t, srv, "/backup").Body.String()
	href := hrefContaining(t, listing, "/backup/preview")
	if href == "" {
		t.Fatalf("the backup listing offers no preview link: %s", listing)
	}

	rec := get(t, srv, href)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", href, rec.Code, rec.Body.String())
	}
	return href, rec.Body.String()
}

// TestTheBackupListingLinksToAPreviewThatRendersIt.
func TestTheBackupListingLinksToAPreviewThatRendersIt(t *testing.T) {
	srv, _ := newBackupServer(t)
	href, body := previewOfALiveBackup(t, srv)

	// The file it is about, named on the screen rather than only in the URL.
	name := strings.TrimPrefix(queryOf(href), "file=")
	if name == "" || !strings.Contains(body, name) {
		t.Errorf("the preview does not name the file %q it describes", name)
	}

	// The counts. One of each catalogue record, the entry, the expense - read
	// off the file rather than out of the database.
	for _, want := range []string{"Acme", "8h 00m"} {
		if !strings.Contains(body, want) {
			t.Errorf("the preview does not mention %q:\n%s", want, body)
		}
	}

	// A template that reads a field the type does not have renders the error
	// into the page rather than failing the request.
	for _, broken := range []string{"<no value>", "can't evaluate field", "%!"} {
		if strings.Contains(body, broken) {
			t.Errorf("the preview rendered a template error (%s):\n%s", broken, body)
		}
	}
}

// TestThePreviewSaysWhatItSaysInBothLanguages.
//
// The screen is not in the i18n sweep's path list, because it needs a file on
// disk before it has anything to say. So it checks itself, with the same two
// checks: a key that does not exist renders as the key, and a message given the
// wrong number of arguments renders fmt's complaint.
func TestThePreviewSaysWhatItSaysInBothLanguages(t *testing.T) {
	srv, _ := newBackupServer(t)
	href, _ := previewOfALiveBackup(t, srv)

	for _, language := range i18n.Languages() {
		t.Run(language.Code, func(t *testing.T) {
			rec := getWithLanguage(t, srv, href, language.Code)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s in %s = %d", href, language.Code, rec.Code)
			}
			body := rec.Body.String()
			for _, marker := range []string{"backup.", "admin.", "expenses.",
				"attachments.", "action.", "nav."} {
				if strings.Contains(body, ">"+marker) {
					t.Errorf("an untranslated key leaked into the preview (%s): %s",
						language.Code, marker)
				}
			}
			if i := strings.Index(body, "%!"); i >= 0 {
				t.Errorf("a message was formatted wrongly (%s): %s",
					language.Code, body[i:min(i+60, len(body))])
			}
		})
	}
}

// TestPreviewingSomethingThatIsNotThere.
//
// The file name comes from a query string, so it is somebody's to choose. Both
// of the wrong choices answer rather than crash: a name that is not in the
// directory, and a name that is a path.
func TestPreviewingSomethingThatIsNotThere(t *testing.T) {
	srv, _ := newBackupServer(t)

	for _, name := range []string{"", "nothing.zip", "../../etc/passwd", "sub/dir.zip"} {
		label := name
		if label == "" {
			label = "no file at all"
		}
		t.Run(label, func(t *testing.T) {
			rec := get(t, srv, "/backup/preview?file="+url.QueryEscape(name))
			if rec.Code == http.StatusOK {
				t.Errorf("GET a preview of %q = 200: %s", name, rec.Body.String())
			}
			if rec.Code >= 500 {
				t.Errorf("GET a preview of %q = %d; a bad name is the caller's "+
					"mistake rather than the server's", name, rec.Code)
			}
		})
	}
}

// TestAPreviewOfSomethingThatIsNotAnArchive.
//
// The backup directory is a directory on a disk, and anything can end up in it
// - a half-copied file, something unrelated. The screen says so instead of
// rendering a page about an archive it could not read.
func TestAPreviewOfSomethingThatIsNotAnArchive(t *testing.T) {
	srv, dir := newBackupServer(t)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the backup directory: %v", err)
	}
	if err := os.WriteFile(dir+"/timetracker-20260316-090000.zip",
		[]byte("not an archive"), 0o600); err != nil {
		t.Fatalf("plant a file: %v", err)
	}

	rec := get(t, srv, "/backup/preview?file=timetracker-20260316-090000.zip")
	if rec.Code == http.StatusOK {
		t.Errorf("a file that is not an archive previewed as one: %s", rec.Body.String())
	}
}
