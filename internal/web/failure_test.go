package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// What every screen does when the database underneath it is gone.
//
// The handlers all end the same way - read, and on an error hand it to fail(),
// which maps it onto a status. That mapping is the only thing standing between
// an unreachable database and a page that renders as though nothing is wrong.
// A screen that swallowed the error would show somebody an empty week, a
// customer list with nothing in it, or a report of no hours, and they would
// believe it: a quiet empty page and a genuinely empty week look identical.
//
// So this closes the database and walks the whole route table. Each screen must
// answer with a server error, must not render a page, and must not leak the
// internals of the failure - the user gets an apology, the operator gets the
// detail in the log.
//
// It is one test for the whole surface on purpose. The alternative, an error
// case per handler, is forty tests that each assert the same two lines, and the
// thing actually worth catching is a handler added later that forgets them.

// screens is every GET a signed-in person can reach, with the parameters each
// needs to get past its own argument parsing and as far as the database.
var screens = []string{
	"/", "/today", "/today?date=2026-03-16", "/week", "/week?date=2026-03-16",
	"/month", "/month?month=2026-03", "/entries", "/entries?q=anything",
	"/admin", "/approvals", "/approvals/report", "/reports/budgets",
	"/expenses", "/routines", "/tags", "/inbox", "/move", "/settings",
	"/import", "/calendar", "/users",
	"/entries/1/edit", "/customers/1/edit", "/projects/1/edit", "/assignments/1/edit",
	"/terms/customer/1", "/terms/project/1",
	"/export/csv", "/export/json", "/export/ical",
}

// writes is every POST, with a form good enough to reach the database.
var writes = []struct {
	path string
	form url.Values
}{
	{"/entries", url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
		"start": {"09:00"}, "duration": {"2h"}}},
	{"/entries/quick", url.Values{"text": {"2h Development"}}},
	{"/entries/1", url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
		"start": {"09:00"}, "duration": {"2h"}}},
	{"/entries/1/move", url.Values{"start": {"10:00"}}},
	{"/entries/1/delete", nil},
	{"/timers/start", url.Values{"assignment_id": {"1"}}},
	{"/timers/1/stop", nil},
	{"/timers/stop-all", nil},
	{"/timers/switch", url.Values{"assignment_id": {"1"}}},
	{"/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}}},
	{"/customers/1", url.Values{"name": {"Acme"}, "currency": {"EUR"}}},
	{"/customers/1/archive", nil},
	{"/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"}}},
	{"/projects/1", url.Values{"customer_id": {"1"}, "name": {"Migration"}}},
	{"/projects/1/archive", nil},
	{"/assignments", url.Values{"project_id": {"1"}, "name": {"Development"}}},
	{"/assignments/1", url.Values{"project_id": {"1"}, "name": {"Development"}}},
	{"/assignments/1/archive", nil},
	{"/assignments/1/favourite", nil},
	{"/terms/customer/1", url.Values{"effective_from": {"2026-01-01"}}},
	{"/terms/customer/1/delete", url.Values{"terms_id": {"1"}}},
	{"/expenses", url.Values{"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"category": {"travel"}, "amount": {"10"}}},
	{"/expenses/1", url.Values{"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"category": {"travel"}, "amount": {"10"}}},
	{"/expenses/1/delete", nil},
	{"/attachments/1/delete", nil},
	{"/inbox/entries/1/accept", nil},
	{"/inbox/entries/1/reject", url.Values{"reason": {"no"}}},
	{"/inbox/expenses/1/accept", nil},
	{"/inbox/expenses/1/reject", url.Values{"reason": {"no"}}},
	{"/week/submit", url.Values{"week": {"2026-03-16"}}},
	{"/week/withdraw", url.Values{"week": {"2026-03-16"}}},
	{"/approvals/approve", url.Values{"user_id": {"1"}, "week": {"2026-03-16"}}},
	{"/approvals/reject", url.Values{"user_id": {"1"}, "week": {"2026-03-16"}, "note": {"no"}}},
	{"/approvals/reopen", url.Values{"user_id": {"1"}, "week": {"2026-03-16"}}},
	{"/days/copy", nil},
	{"/weeks/copy", nil},
	{"/routines", url.Values{"assignment_id": {"1"}, "name": {"Standup"},
		"duration": {"15m"}, "weekdays": {"1"}}},
	{"/routines/1/apply", nil},
	{"/routines/1/delete", nil},
	{"/routines/apply-all", nil},
	{"/tags/1", url.Values{"name": {"incident"}}},
	{"/tags/1/delete", nil},
	{"/search/reindex", nil},
	{"/move", url.Values{"from_assignment": {"1"}, "to_assignment": {"2"}}},
	{"/settings", url.Values{"currency": {"EUR"}, "week_start": {"1"}}},
	{"/preferences/theme", url.Values{"theme": {"dark"}}},
	{"/preferences/language", url.Values{"language": {"sv"}}},
	{"/reminders/empty_day/dismiss", url.Values{"scope": {"2026-03-16"}}},
	{"/idle", url.Values{"entry_id": {"1"}, "from": {"2026-03-16T09:00:00Z"},
		"to": {"2026-03-16T09:30:00Z"}, "source": {"untouched"}}},
	{"/idle/1/resolve", url.Values{"resolution": {"keep"}}},
}

// newServerWithADeadDatabase is the fixture with its database closed from
// under it, and with the acting user remembered rather than re-read.
//
// Remembering the user is what makes this test about the handlers. Local mode
// re-reads the identity from the database on every request, so with the
// database gone the middleware would refuse before any handler ran and this
// would only ever test the middleware. Holding the user means each request gets
// all the way into its handler, whose own read then fails - which is the error
// path every screen has and the one worth proving.
func newServerWithADeadDatabase(t *testing.T) *Server {
	t.Helper()
	if testing.Short() {
		t.Skip("web tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "doomed.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(db, auth.SingleUserAuthorizer{}, logger, time.Now)
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

	srv, err := New(svc, config.Config{Mode: config.ModeLocal}, logger, Options{
		Identity: func(*http.Request) (domain.User, error) { return user, nil },
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}

	// Working first, so a failure below cannot be a route that was already
	// broken.
	if rec := get(t, srv, "/today"); rec.Code != http.StatusOK {
		t.Fatalf("GET /today before closing the database = %d\n%s", rec.Code, rec.Body.String())
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the database: %v", err)
	}
	return srv
}

// TestEveryScreenFailsLoudlyWhenTheDatabaseIsGone.
//
// See the note at the top of this file.
func TestEveryScreenFailsLoudlyWhenTheDatabaseIsGone(t *testing.T) {
	srv := newServerWithADeadDatabase(t)

	t.Run("screens", func(t *testing.T) {
		for _, path := range screens {
			t.Run(path, func(t *testing.T) {
				rec := get(t, srv, path)
				assertFailedCleanly(t, rec.Code, rec.Body.String(), "GET "+path)
			})
		}
	})

	t.Run("writes", func(t *testing.T) {
		for _, write := range writes {
			t.Run(write.path, func(t *testing.T) {
				form := write.form
				if form == nil {
					form = url.Values{}
				}
				rec := post(t, srv, write.path, form)
				assertFailedCleanly(t, rec.Code, rec.Body.String(), "POST "+write.path)
			})
		}
	})
}

// assertFailedCleanly is the uniform check: an error status, no page, and
// nothing about the internals of the failure.
func assertFailedCleanly(t *testing.T, code int, body, what string) {
	t.Helper()

	if code < 400 {
		t.Errorf("%s = %d with the database gone, want an error - a quiet empty "+
			"page and a genuinely empty week look identical to the person reading it",
			what, code)
		return
	}

	// Not a rendered screen. A half-drawn page is worse than an error, because
	// the parts that did render look authoritative.
	if strings.Contains(body, "<main") || strings.Contains(body, "<table") {
		t.Errorf("%s rendered a page alongside its %d:\n%s", what, code, truncate(body))
	}

	// And nothing about how it broke. Internal errors name SQL, file paths and
	// package structure; the user gets an apology and the log gets the rest.
	for _, leak := range []string{
		"sql:", "SELECT", "INSERT", "database is closed",
		"timetracker/internal", "goroutine",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("%s leaked %q into its response:\n%s", what, leak, truncate(body))
		}
	}
}

// truncate keeps a failure message readable when a whole page comes back.
func truncate(body string) string {
	const limit = 400
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "…"
}

// TestTheRouteTableIsCoveredByTheFailureSweep.
//
// The sweep above is only as good as its two lists, and a list in a test file
// stops being complete the week after it is written. This reads the route table
// out of the package's own source and requires every route to appear in one of
// them, so a handler added later is checked for free.
func TestTheRouteTableIsCoveredByTheFailureSweep(t *testing.T) {
	registered := registeredRoutes(t)
	if len(registered) == 0 {
		t.Fatal("found no routes, so this test is not reading routes.go")
	}

	covered := map[string]bool{}
	for _, path := range screens {
		covered["GET "+routePattern(path)] = true
	}
	for _, write := range writes {
		covered["POST "+routePattern(write.path)] = true
	}

	// Routes with nothing behind them to break: static assets, the health
	// check, and the authentication endpoints, which are covered by their own
	// tests because they answer before reaching a screen.
	exempt := map[string]bool{
		"GET /static/": true, "GET /healthz": true,
		"GET /favicon.ico": true, "GET /apple-touch-icon.png": true,
		"GET /apple-touch-icon-precomposed.png": true, "GET /site.webmanifest": true,
		"GET /login": true, "POST /login": true, "POST /logout": true,
		"GET /auth/oidc/start": true, "GET /auth/oidc/callback": true,
		"POST /users": true, "POST /users/{id}": true, "POST /users/{id}/password": true,
		"POST /members": true, "POST /members/remove": true,
		// Help and the guide are static text with no database behind them.
		"GET /help/{screen}": true, "GET /guide": true, "GET /guide/{topic}": true,
		// Backup and restore move files on disk; they have their own tests, and
		// sweeping them here would write archives.
		"GET /backup": true, "GET /backup/download": true,
		"POST /backup/create": true, "POST /backup/restore": true,
		// Uploads need multipart bodies, which this sweep does not build.
		"POST /attachments/{owner}/{id}": true,
		"GET /attachments/{id}":          true,
		"GET /attachments/{id}/preview":  true,
		"POST /import/preview":           true, "POST /import": true,
		"POST /calendar/preview": true, "POST /calendar": true,
	}

	for _, route := range registered {
		if covered[route] || exempt[route] {
			continue
		}
		t.Errorf("%s is registered and is not in the failure sweep. Add it to "+
			"screens or writes, or to the exempt list with the reason.", route)
	}
}

// registeredRoutes reads the method-and-pattern strings out of routes().
//
// Parsing the source rather than asking the mux: Go's ServeMux does not
// enumerate its patterns, and a second hand-written list of routes would go
// stale in exactly the way this test exists to prevent.
func registeredRoutes(t *testing.T) []string {
	t.Helper()

	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}

	var routes []string
	seen := map[string]bool{}
	for _, match := range routeLiteral.FindAllStringSubmatch(string(source), -1) {
		route := match[1]
		if !seen[route] {
			seen[route] = true
			routes = append(routes, route)
		}
	}
	sort.Strings(routes)
	return routes
}

// routeLiteral matches the "METHOD /path" string literals routes() registers.
var routeLiteral = regexp.MustCompile(`"((?:GET|POST|PUT|DELETE) /[^"]*)"`)

// routePattern turns a request path back into the pattern it was registered
// under: query string dropped, and each numeric or enumerated segment replaced
// by the wildcard that matched it.
func routePattern(path string) string {
	if cut := strings.IndexByte(path, '?'); cut >= 0 {
		path = path[:cut]
	}
	if path == "/" {
		return "/{$}"
	}

	segments := strings.Split(path, "/")
	for i, segment := range segments {
		switch {
		case segment == "":
		case isAllDigits(segment):
			segments[i] = "{id}"
		default:
			// The named segments that are values rather than literals. Each is
			// identified by what precedes it, which is how the route table
			// spells them.
			if i > 0 {
				switch segments[i-1] {
				case "terms":
					segments[i] = "{scope}"
				case "export":
					segments[i] = "{format}"
				case "reminders":
					segments[i] = "{kind}"
				}
			}
		}
	}
	return strings.Join(segments, "/")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
