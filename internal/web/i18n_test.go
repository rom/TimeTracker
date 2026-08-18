package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/i18n"
)

// getWithLanguage issues a GET carrying an Accept-Language header.
func getWithLanguage(t *testing.T, srv *Server, path, language string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if language != "" {
		req.Header.Set("Accept-Language", language)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestLanguageNegotiation: a Swedish browser gets a Swedish page without having
// to configure anything.
func TestLanguageNegotiation(t *testing.T) {
	srv, _ := newTestServer(t)

	swedish := getWithLanguage(t, srv, "/today", "sv-SE,sv;q=0.9,en;q=0.8").Body.String()
	if !strings.Contains(swedish, `lang="sv"`) {
		t.Error("a Swedish browser did not get a Swedish document")
	}
	if !strings.Contains(swedish, "Idag") {
		t.Error("the navigation was not translated")
	}
	// The lang attribute is what a screen reader uses to choose a voice, so a
	// Swedish page claiming English is worse than useless.
	if strings.Contains(swedish, `lang="en"`) {
		t.Error("the document claims English while rendering Swedish")
	}

	english := getWithLanguage(t, srv, "/today", "en-GB,en;q=0.9").Body.String()
	if !strings.Contains(english, `lang="en"`) {
		t.Error("an English browser did not get an English document")
	}
	if !strings.Contains(english, "Today") {
		t.Error("the navigation is not in English")
	}
}

// TestQualityValuesAreRespected: browsers commonly list a language second with a
// higher weight, and taking the first tag would ignore the user's real
// preference.
func TestQualityValuesAreRespected(t *testing.T) {
	if got := i18n.Negotiate("de;q=0.9,sv;q=1.0"); got != "sv" {
		t.Errorf("Negotiate = %q, want sv - the higher quality value should win", got)
	}
	if got := i18n.Negotiate("fr-FR,de-DE"); got != "en" {
		t.Errorf("Negotiate with no supported language = %q, want the default", got)
	}
	if got := i18n.Negotiate(""); got != "en" {
		t.Errorf("Negotiate with no header = %q, want the default", got)
	}
	// Region variants map to the base language: a Swedish speaker in Finland
	// should still get Swedish.
	if got := i18n.Negotiate("sv-FI"); got != "sv" {
		t.Errorf("Negotiate(sv-FI) = %q, want sv", got)
	}
}

// TestStoredPreferenceBeatsTheHeader: an explicit choice must win over a browser
// configured by somebody else.
func TestStoredPreferenceBeatsTheHeader(t *testing.T) {
	srv, _ := newTestServer(t)

	if rec := post(t, srv, "/preferences/language", url.Values{
		"language": {"sv"},
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("setting the language = %d: %s", rec.Code, rec.Body.String())
	}

	// The browser asks for English; the stored preference says Swedish.
	body := getWithLanguage(t, srv, "/today", "en-GB,en;q=0.9").Body.String()
	if !strings.Contains(body, `lang="sv"`) {
		t.Error("the stored preference was overridden by the Accept-Language header")
	}
}

// TestLanguageAllowList: the value ends up in the document's lang attribute.
func TestLanguageAllowList(t *testing.T) {
	srv, _ := newTestServer(t)

	if rec := post(t, srv, "/preferences/language", url.Values{
		"language": {"\"><script>alert(1)</script>"},
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown language was accepted: %d", rec.Code)
	}
}

// TestSwedishNumberFormatting: a Swedish reader expects a decimal comma, and
// showing them a point reads as an error rather than as English.
func TestSwedishFormatting(t *testing.T) {
	swedish := i18n.NewPrinter("sv")
	english := i18n.NewPrinter("en")

	// U+00A0, a non-breaking space: the Swedish convention, and it stops a
	// figure wrapping across a line break.
	if got := swedish.FormatDecimal("1234.50"); got != "1\u00a0234,50" {
		t.Errorf("Swedish decimal = %q, want %q", got, "1\u00a0234,50")
	}
	if got := english.FormatDecimal("1234.50"); got != "1,234.50" {
		t.Errorf("English decimal = %q, want %q", got, "1,234.50")
	}

	// Durations use translated unit labels, not just translated words around
	// English ones.
	if got := swedish.FormatDuration(5400); got != "1 tim 30 min" {
		t.Errorf("Swedish duration = %q, want %q", got, "1 tim 30 min")
	}
	if got := english.FormatDuration(5400); got != "1h 30m" {
		t.Errorf("English duration = %q, want %q", got, "1h 30m")
	}

	if got := swedish.FormatMoney(123450, "SEK"); got != "1\u00a0234,50\u00a0SEK" {
		t.Errorf("Swedish money = %q, want %q", got, "1\u00a0234,50\u00a0SEK")
	}
}

// TestPluralForms: "1 timer running" and "2 timers running" are different
// sentences, and getting it wrong is the most visible kind of translation bug.
func TestPluralForms(t *testing.T) {
	english := i18n.NewPrinter("en")
	if got := english.N("timer.running", 1); got != "1 timer running" {
		t.Errorf("singular = %q", got)
	}
	if got := english.N("timer.running", 3); got != "3 timers running" {
		t.Errorf("plural = %q", got)
	}

	swedish := i18n.NewPrinter("sv")
	if got := swedish.N("timer.running", 1); got != "1 tidtagning igång" {
		t.Errorf("Swedish singular = %q", got)
	}
	if got := swedish.N("timer.running", 3); got != "3 tidtagningar igång" {
		t.Errorf("Swedish plural = %q", got)
	}
}

// TestEveryScreenRendersInEveryLanguage: a missing key or a bad format verb
// takes a whole screen down, and it should never reach a user in any language.
func TestEveryScreenRendersInEveryLanguage(t *testing.T) {
	srv, _ := newTestServer(t)

	// Give the screens something to render.
	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}})

	paths := []string{"/today", "/week", "/entries", "/admin",
		"/help/today", "/help/week", "/help/entries", "/help/admin", "/help/users"}

	for _, language := range i18n.Languages() {
		for _, path := range paths {
			t.Run(language.Code+path, func(t *testing.T) {
				rec := getWithLanguage(t, srv, path, language.Code)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s in %s = %d: %s", path, language.Code, rec.Code, rec.Body.String())
				}
				// An untranslated key renders as the key itself, which is ugly
				// but easy to spot - so spot it here rather than in production.
				body := rec.Body.String()
				for _, marker := range []string{"help.", "nav.", "action.", "totals."} {
					if strings.Contains(body, ">"+marker) {
						t.Errorf("an untranslated key leaked into %s (%s): %s",
							path, language.Code, marker)
					}
				}
			})
		}
	}
}

// TestCatalogueParity: every key in the default catalogue must exist in every
// other, or users of that language silently fall back to English in places
// nobody noticed.
func TestCatalogueParity(t *testing.T) {
	if err := i18n.LoadError(); err != nil {
		t.Fatalf("the catalogues did not load: %v", err)
	}

	reference := i18n.NewPrinter(i18n.DefaultLanguage)
	for _, language := range i18n.Languages() {
		if language.Code == i18n.DefaultLanguage {
			continue
		}
		printer := i18n.NewPrinter(language.Code)
		for _, key := range i18n.Keys(i18n.DefaultLanguage) {
			if printer.Missing(key) {
				t.Errorf("%s is missing the key %q (English: %q)",
					language.Code, key, reference.T(key))
			}
		}
	}
}
