package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/i18n"
)

// doRequest serves one prepared request, for the cases that need a header and
// a cookie together.
func doRequest(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// The user guide.
//
// Documentation that is wrong is worse than none, so what is tested here is
// less the rendering than the promises: that every topic exists in every
// language, that the steps render as steps, and that a topic which cannot apply
// is not offered.

// TestGuideCoversEveryTopicInEveryLanguage. A topic with no catalogue entry
// renders as its own key, which is a page of gibberish rather than an error -
// exactly the sort of thing that ships unnoticed.
func TestGuideCoversEveryTopicInEveryLanguage(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	cookie := signIn(t, srv)

	for _, language := range i18n.Languages() {
		printer := i18n.NewPrinter(language.Code)
		for _, topic := range guideTopics {
			for _, suffix := range []string{".title", ".summary", ".body"} {
				key := "guide." + topic.Key + suffix
				if printer.Missing(key) {
					t.Errorf("%s has no %s", language.Code, key)
				}
			}
		}

		req := httptest.NewRequest(http.MethodGet, "/guide", nil)
		req.Header.Set("Accept-Language", language.Code)
		req.AddCookie(cookie)
		rec := doRequest(srv, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /guide in %s = %d", language.Code, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "guide.") {
			t.Errorf("an untranslated guide key leaked into the %s page", language.Code)
		}
		// Steps are the point of a how-to. If the markup renderer stopped
		// producing lists, the instructions would still be there but would read
		// as prose, and an ordered procedure would look optional.
		if !strings.Contains(body, "<ol>") {
			t.Errorf("the %s guide renders no numbered steps", language.Code)
		}
	}
}

// TestGuideAnswersTheProxyQuestion. The guide exists because a per-screen help
// panel cannot reach somebody who does not yet know which screen they need -
// recording time for a colleague being the case in point. So the topic has to
// be there, and it has to say the thing that surprises people: it is a proposal
// until they accept it.
func TestGuideAnswersTheProxyQuestion(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	cookie := signIn(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/guide/proxy", nil)
	req.AddCookie(cookie)
	rec := doRequest(srv, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /guide/proxy = %d", rec.Code)
	}

	body := rec.Body.String()
	for _, promise := range []string{"proposal", "Inbox", "@name", "same project"} {
		if !strings.Contains(body, promise) {
			t.Errorf("the proxy guide does not mention %q", promise)
		}
	}
}

// TestGuideOmitsTopicsThatCannotApply: in local mode there is nobody to record
// time for and nobody to approve anything, and offering instructions for
// controls that are not shown sends people hunting for them.
func TestGuideOmitsTopicsThatCannotApply(t *testing.T) {
	srv, _ := newTestServer(t)

	body := get(t, srv, "/guide").Body.String()
	for _, absent := range []string{`href="#proxy"`, `href="#approve"`} {
		if strings.Contains(body, absent) {
			t.Errorf("the single-user guide offers %s", absent)
		}
	}
	// The topics that do apply are still all there.
	for _, present := range []string{`href="#record"`, `href="#correct"`, `href="#submit"`} {
		if !strings.Contains(body, present) {
			t.Errorf("the single-user guide is missing %s", present)
		}
	}
}

// TestGuideFallsBackRatherThanErroring: someone who has reached a guide URL is
// asking for instructions, and an error page is a poor answer to that.
func TestGuideFallsBackRatherThanErroring(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := get(t, srv, "/guide/does-not-exist")
	if rec.Code != http.StatusOK {
		t.Fatalf("an unknown topic = %d, want the whole guide", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="#record"`) {
		t.Error("the fallback did not render the guide")
	}
}

// TestGuideMarkupRendersListsAndNothingElse. The renderer inserts tags around
// already-escaped text, so the only way markup can become a hole is if it stops
// escaping first. That, and the list rules, are what this pins.
func TestGuideMarkupRendersListsAndNothingElse(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		contains []string
		absent   []string
	}{
		{
			name:     "numbered lines become an ordered list",
			body:     "1. first\n2. second\n3. third",
			contains: []string{"<ol>", "<li>first</li>", "<li>third</li>"},
			absent:   []string{"<p>1."},
		},
		{
			name:     "dashed lines become a bullet list",
			body:     "- one\n- two",
			contains: []string{"<ul>", "<li>one</li>"},
		},
		{
			// A paragraph that merely mentions a number stays prose, or every
			// sentence beginning with a date would become a list.
			name:     "a mixed block stays prose",
			body:     "1. first\nand then some prose",
			contains: []string{"<p>"},
			absent:   []string{"<ol>"},
		},
		{
			name:     "a single numbered line is not a list",
			body:     "1. on its own",
			contains: []string{"<p>"},
			absent:   []string{"<ol>"},
		},
		{
			name:     "markup works inside a list item",
			body:     "- press **Save**\n- then `stop`",
			contains: []string{"<strong>Save</strong>", "<code>stop</code>"},
		},
		{
			// The whole safety property in one case: text is escaped before any
			// tag of ours is inserted, in list items as well as paragraphs.
			name:     "html in the catalogue is escaped, not executed",
			body:     "- <script>alert(1)</script>\n- <b>bold</b>",
			contains: []string{"&lt;script&gt;", "&lt;b&gt;"},
			absent:   []string{"<script>", "<b>"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(renderHelpBody(tc.body))
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}
