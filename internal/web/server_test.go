package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/service"
	"github.com/rom/timetracker/internal/store"
)

// newTestServer wires a complete server over a temporary database, exactly as
// local mode does. These are HTTP-level tests with no browser involved.
func newTestServer(t *testing.T) (*Server, domain.User) {
	t.Helper()
	if testing.Short() {
		t.Skip("web tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(db, auth.SingleUserAuthorizer{}, logger, time.Now)

	user, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Test User", Role: domain.RoleAdmin,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	srv, err := New(svc, config.Config{Mode: config.ModeLocal}, logger,
		func(*http.Request) (domain.User, error) { return user, nil })
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return srv, user
}

// get issues a GET and returns the recorder.
func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// post issues a form POST and returns the recorder.
func post(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestEveryScreenRenders is the broad smoke test: a template that fails to parse
// or references a field that does not exist takes a whole screen down, and that
// should never reach a user.
func TestEveryScreenRenders(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, path := range []string{"/", "/today", "/week", "/entries", "/admin", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, srv, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200\n%s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestSecurityHeaders: the headers are set in both modes, because the browser
// showing a local page is the same browser that has other sites open.
func TestSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/today")

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy header")
	}
	// No unsafe-inline for scripts: all JavaScript is served as a file, which is
	// what closes the common route from an injected string to executed code.
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP allows inline script: %s", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff header")
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Error("CSP does not forbid framing")
	}
}

// TestFullFlow walks the path a user actually takes, through HTTP.
func TestFullFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	if rec := post(t, srv, "/customers", url.Values{
		"name": {"Acme AB"}, "currency": {"SEK"}, "rate": {"1250"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create customer = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"},
		"rounding_rule": {"up/900/0"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create project = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, "/assignments", url.Values{
		"project_id": {"1"}, "name": {"Development"}, "billable": {"on"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create assignment = %d: %s", rec.Code, rec.Body.String())
	}

	// Two timers at once: the behaviour ASR-001 exists for.
	for i := 0; i < 2; i++ {
		if rec := post(t, srv, "/timers/start", url.Values{
			"assignment_id": {"1"},
		}); rec.Code != http.StatusSeeOther {
			t.Fatalf("start timer = %d: %s", rec.Code, rec.Body.String())
		}
	}

	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, "2 running") {
		t.Error("the running-timer bar does not show both timers")
	}
	if !strings.Contains(body, "Development") {
		t.Error("the assignment is missing from the day view")
	}
}

// TestExports checks that both formats are produced and that the unimplemented
// ones answer honestly rather than serving a broken file.
func TestExports(t *testing.T) {
	srv, _ := newTestServer(t)

	csv := get(t, srv, "/export/csv")
	if csv.Code != http.StatusOK {
		t.Fatalf("CSV export = %d", csv.Code)
	}
	if ct := csv.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("CSV content type = %q", ct)
	}
	// The BOM is what stops Excel on Windows from mangling non-ASCII names.
	if !strings.HasPrefix(csv.Body.String(), "\xef\xbb\xbf") {
		t.Error("CSV export has no UTF-8 BOM")
	}
	if !strings.Contains(csv.Header().Get("Content-Disposition"), "attachment") {
		t.Error("CSV is not served as a download")
	}

	jsonRec := get(t, srv, "/export/json")
	if jsonRec.Code != http.StatusOK {
		t.Fatalf("JSON export = %d", jsonRec.Code)
	}
	if !strings.Contains(jsonRec.Body.String(), `"schema_version"`) {
		t.Error("JSON export carries no schema version")
	}

	if rec := get(t, srv, "/export/pdf"); rec.Code != http.StatusNotImplemented {
		t.Errorf("PDF export = %d, want 501 until layer 5", rec.Code)
	}
	if rec := get(t, srv, "/export/xml"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown format = %d, want 400", rec.Code)
	}
}

// TestThemeAllowList: the theme value is written into an HTML attribute, so only
// known themes are accepted.
func TestThemeAllowList(t *testing.T) {
	srv, _ := newTestServer(t)

	if rec := post(t, srv, "/preferences/theme", url.Values{
		"theme": {"autumn"},
	}); rec.Code != http.StatusNoContent {
		t.Errorf("setting a known theme = %d", rec.Code)
	}
	if rec := post(t, srv, "/preferences/theme", url.Values{
		"theme": {"<script>alert(1)</script>"},
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown theme was accepted: %d", rec.Code)
	}
}

// TestInvalidInputIsRejected: bad input gets a 400 and an explanation, not a 500.
func TestInvalidInputIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)

	if rec := post(t, srv, "/timers/start", url.Values{}); rec.Code != http.StatusBadRequest {
		t.Errorf("starting a timer with no assignment = %d, want 400", rec.Code)
	}
	if rec := post(t, srv, "/entries/quick", url.Values{
		"text": {"this line has no duration"},
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("unparseable quick add = %d, want 400", rec.Code)
	}
}

// TestHTMXRequestsGetRefreshInsteadOfRedirect: a background submit is answered
// with an instruction to redraw, so the page updates in place.
func TestHTMXRequestsGetRefreshInsteadOfRedirect(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})

	form := url.Values{"assignment_id": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/timers/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("HTMX submit = %d, want 204", rec.Code)
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Error("no HX-Refresh header on an HTMX mutation")
	}
}

// TestStaticAssetsAreServed: the CSS and JS are embedded, so a copied binary
// serves them with nothing beside it.
func TestStaticAssetsAreServed(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, path := range []string{"/static/css/app.css", "/static/js/app.js", "/static/icons/favicon.svg"} {
		rec := get(t, srv, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", path)
		}
	}
}

// TestAllThemesDefineEveryToken: components reference semantic tokens, so a
// theme that omits one silently inherits a colour from another theme's palette.
// This catches that at build time rather than in front of a user.
func TestAllThemesDefineEveryToken(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	text := string(css)

	required := []string{
		"--surface:", "--surface-raised:", "--surface-sunken:",
		"--border:", "--border-strong:", "--text:", "--text-muted:",
		"--accent:", "--accent-text:", "--accent-soft:",
		"--danger:", "--success:", "--warning:",
	}
	// The light theme is the base on :root; the rest override it by attribute.
	for _, theme := range []string{"dark", "gold", "sand", "spring", "autumn", "contrast"} {
		block := themeBlock(text, theme)
		if block == "" {
			t.Errorf("no token block for theme %q", theme)
			continue
		}
		for _, token := range required {
			if !strings.Contains(block, token) {
				t.Errorf("theme %q does not define %s", theme, strings.TrimSuffix(token, ":"))
			}
		}
	}
}

// themeBlock extracts the first [data-theme="name"] { ... } block.
func themeBlock(css, theme string) string {
	marker := `[data-theme="` + theme + `"] {`
	start := strings.Index(css, marker)
	if start < 0 {
		return ""
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		return ""
	}
	return css[start : start+end]
}
