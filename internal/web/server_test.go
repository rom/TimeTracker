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
	"github.com/rom/timetracker/internal/blob"
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

	// A blob store, as the real server wires one. Without it every attachment
	// operation is refused as unconfigured - which would make an upload test
	// pass for entirely the wrong reason.
	blobs, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	svc = svc.WithBlobs(blobs)

	user, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Test User", Role: domain.RoleAdmin,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Re-read on each request, exactly as local mode does, so a preference
	// change takes effect on the next page load rather than being invisible.
	srv, err := New(svc, config.Config{Mode: config.ModeLocal}, logger, Options{
		Identity: func(r *http.Request) (domain.User, error) {
			return db.GetUser(r.Context(), user.ID)
		},
	})
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
	if !strings.Contains(body, "2 timers running") {
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

	pdf := get(t, srv, "/export/pdf")
	if pdf.Code != http.StatusOK {
		t.Fatalf("PDF export = %d", pdf.Code)
	}
	if ct := pdf.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("PDF content type = %q", ct)
	}
	if !strings.HasPrefix(pdf.Body.String(), "%PDF-") {
		t.Error("the PDF export is not a PDF")
	}

	docx := get(t, srv, "/export/docx")
	if docx.Code != http.StatusOK {
		t.Fatalf("DOCX export = %d", docx.Code)
	}
	// A .docx is a zip, so it starts with the local file header signature.
	if !strings.HasPrefix(docx.Body.String(), "PK\x03\x04") {
		t.Error("the DOCX export is not a zip archive")
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

// ---------------------------------------------------------- server mode ----

// newServerModeTestServer wires the server the way --mode=server does: the RBAC
// authoriser, real sessions, and CSRF enforcement.
func newServerModeTestServer(t *testing.T) (*Server, *service.Accounts) {
	t.Helper()
	if testing.Short() {
		t.Skip("server-mode tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(db, auth.RoleAuthorizer{IsProjectMember: db.IsProjectMember}, logger, time.Now)
	blobs, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	svc = svc.WithBlobs(blobs)

	accounts := service.NewAccounts(db, svc, time.Now)

	if _, err := accounts.BootstrapFirstAdmin(ctx, service.NewUserInput{
		DisplayName: "Admin", Email: "admin@example.com", Password: "a-long-enough-password",
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	srv, err := New(svc, config.Config{Mode: config.ModeServer}, logger, Options{
		Identity: func(r *http.Request) (domain.User, error) {
			cookie, err := r.Cookie(auth.SessionCookieName)
			if err != nil {
				return domain.User{}, auth.ErrUnauthenticated
			}
			user, _, err := accounts.ResolveSession(r.Context(), cookie.Value)
			return user, err
		},
		Accounts: accounts,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return srv, accounts
}

// signIn performs a real login and returns the session cookie.
func signIn(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	rec := post(t, srv, "/login", url.Values{
		"email": {"admin@example.com"}, "password": {"a-long-enough-password"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			return cookie
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

// TestUnauthenticatedReachesNoData is the direct check of ASR-005: an
// unauthenticated request to any route other than login, static assets and
// health must receive a redirect, never data.
func TestUnauthenticatedReachesNoData(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	for _, path := range []string{
		"/", "/today", "/week", "/entries", "/admin", "/users",
		"/export/csv", "/export/json",
	} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, srv, path)
			if rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s = %d, want a redirect or 401", path, rec.Code)
			}
			// Not merely the right status: the body must contain no data.
			body := rec.Body.String()
			for _, leak := range []string{"admin@example.com", "csrf_token", "<table"} {
				if strings.Contains(body, leak) {
					t.Errorf("unauthenticated response to %s contained %q", path, leak)
				}
			}
		})
	}
}

// TestCSRFIsEnforced: an authenticated request without the token must be
// refused, which is what stops another site driving the application with the
// browser's cookies.
func TestCSRFIsEnforced(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	cookie := signIn(t, srv)

	postWithCookie := func(form url.Values) int {
		req := httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := postWithCookie(url.Values{"name": {"Acme"}}); code != http.StatusForbidden {
		t.Errorf("a POST with no CSRF token = %d, want 403", code)
	}
	if code := postWithCookie(url.Values{
		"name": {"Acme"}, "csrf_token": {"not-the-right-token"},
	}); code != http.StatusForbidden {
		t.Errorf("a POST with a wrong CSRF token = %d, want 403", code)
	}

	// With the real token it goes through.
	token := csrfTokenFor(t, srv, cookie)
	if code := postWithCookie(url.Values{
		"name": {"Acme"}, "currency": {"EUR"}, "csrf_token": {token},
	}); code != http.StatusSeeOther {
		t.Errorf("a POST with the correct CSRF token = %d, want 303", code)
	}
}

// csrfTokenFor reads the token the server rendered into a page's forms.
func csrfTokenFor(t *testing.T, srv *Server, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	const marker = `name="csrf_token" value="`
	body := rec.Body.String()
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("no CSRF token in the rendered page (status %d)", rec.Code)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("malformed CSRF token in the page")
	}
	return rest[:end]
}

// TestSessionCookieAttributes: the cookie is the credential, so its flags are
// part of the security posture rather than a detail.
func TestSessionCookieAttributes(t *testing.T) {
	srv, _ := newServerModeTestServer(t)
	cookie := signIn(t, srv)

	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by script")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Domain != "" {
		t.Errorf("the cookie is not host-only: Domain = %q", cookie.Domain)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want /", cookie.Path)
	}
}

// TestLoginDoesNotEnumerateAccounts: the response to an unknown address and to a
// wrong password must be identical.
func TestLoginDoesNotEnumerateAccounts(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	unknown := post(t, srv, "/login", url.Values{
		"email": {"nobody@example.com"}, "password": {"some-long-password"},
	})
	wrong := post(t, srv, "/login", url.Values{
		"email": {"admin@example.com"}, "password": {"the-wrong-password"},
	})

	if unknown.Code != wrong.Code {
		t.Errorf("different statuses: unknown %d, wrong password %d", unknown.Code, wrong.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Error("the login page distinguishes an unknown account from a wrong password")
	}
}

// TestLoginRedirectIsNotOpen: ?next= must not become a redirect to another site.
func TestLoginRedirectIsNotOpen(t *testing.T) {
	srv, _ := newServerModeTestServer(t)

	for _, hostile := range []string{
		"https://evil.example/phish",
		"//evil.example/phish",
		"http://evil.example",
	} {
		rec := post(t, srv, "/login", url.Values{
			"email": {"admin@example.com"}, "password": {"a-long-enough-password"},
			"next": {hostile},
		})
		if location := rec.Header().Get("Location"); strings.Contains(location, "evil.example") {
			t.Errorf("next=%q produced an off-site redirect to %q", hostile, location)
		}
	}
}

// TestLogoutRevokesServerSide: forgetting the cookie is not enough, because the
// cookie is still a valid credential until the session row is gone.
func TestLogoutRevokesServerSide(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	cookie := signIn(t, srv)
	token := csrfTokenFor(t, srv, cookie)

	req := httptest.NewRequest(http.MethodPost, "/logout",
		strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d", rec.Code)
	}
	// The old cookie value must no longer resolve, independently of the browser.
	if _, _, err := accounts.ResolveSession(context.Background(), cookie.Value); err == nil {
		t.Error("the session survived sign-out on the server side")
	}
}

// TestMemberCannotReachUserAdministration, through HTTP rather than the service
// alone: hiding a nav link is presentation, never enforcement.
func TestMemberCannotReachUserAdministration(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	adminCookie := signIn(t, srv)
	token := csrfTokenFor(t, srv, adminCookie)

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(url.Values{
		"display_name": {"Member"}, "email": {"member@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
		"csrf_token": {token},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("creating the member = %d: %s", rec.Code, rec.Body.String())
	}

	memberLogin, err := accounts.Login(context.Background(), service.LoginRequest{
		Email: "member@example.com", Password: "a-long-enough-password", IP: "203.0.113.1",
	})
	if err != nil {
		t.Fatalf("member login: %v", err)
	}
	memberCookie := &http.Cookie{Name: auth.SessionCookieName, Value: memberLogin.CookieValue}

	listReq := httptest.NewRequest(http.MethodGet, "/users", nil)
	listReq.AddCookie(memberCookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)

	if listRec.Code == http.StatusOK {
		t.Errorf("a member reached the user administration screen: %d", listRec.Code)
	}
	if strings.Contains(listRec.Body.String(), "admin@example.com") {
		t.Error("a member saw another account's email address")
	}
}

// TestFormsAcceptBothEncodings is a regression test for a real bug.
//
// The browser's fetch(FormData) sends multipart/form-data. r.ParseForm does not
// parse a multipart body but does set r.Form, so the later r.FormValue never
// falls back to the multipart parser and every field arrived empty - the handler
// then rejected a fully completed form with "customer name is required".
//
// Both encodings must therefore produce identical results.
func TestFormsAcceptBothEncodings(t *testing.T) {
	srv, _ := newTestServer(t)

	// Plain form post, as a browser without JavaScript sends it.
	if rec := post(t, srv, "/customers", url.Values{
		"name": {"MCF"}, "currency": {"SEK"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("urlencoded = %d: %s", rec.Code, rec.Body.String())
	}

	// Multipart, as fetch(FormData) sends it.
	var body strings.Builder
	const boundary = "testboundary"
	for field, value := range map[string]string{"name": "MCF Multipart", "currency": "SEK"} {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(`Content-Disposition: form-data; name="` + field + `"` + "\r\n\r\n")
		body.WriteString(value + "\r\n")
	}
	body.WriteString("--" + boundary + "--\r\n")

	req := httptest.NewRequest(http.MethodPost, "/customers", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("multipart = %d, want 303: %s", rec.Code, rec.Body.String())
	}

	// Both must actually exist, not merely have been accepted.
	page := get(t, srv, "/admin").Body.String()
	for _, want := range []string{"MCF", "MCF Multipart"} {
		if !strings.Contains(page, want) {
			t.Errorf("customer %q was not created", want)
		}
	}
}
