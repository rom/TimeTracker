// Package web is the HTTP layer: routing, middleware, form decoding and
// rendering.
//
// It makes no authorisation decisions and issues no SQL. A handler decodes the
// request, calls one service method, and renders what comes back. That boundary
// is what allows "is every access authorised?" to be answered by reading
// internal/service rather than by auditing every handler here.
// See docs/adr/0012-layered-package-structure.md.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/export"
	"github.com/rom/timetracker/internal/service"
)

// Assets are embedded so the binary is genuinely self-contained: copying one file
// onto a machine yields a working application, with no "forgot the templates"
// failure mode. See docs/adr/0009-embedded-assets-and-migrations.md.
//
//go:embed templates/*.html
var templateFS embed.FS

// The icons under static/icons are drawn by internal/web/icongen, which reads
// its geometry from favicon.svg's viewBox. Regenerate them after changing the
// mark rather than exporting eight files by hand.
//
//go:generate go run ./icongen -dir ./static/icons
//go:embed static
var staticFS embed.FS

// Server holds everything the HTTP layer needs.
type Server struct {
	svc       *service.Service
	cfg       config.Config
	log       *slog.Logger
	templates map[string]*template.Template
	mux       *http.ServeMux
	// identity resolves the acting user for a request. In local mode it returns
	// the single user; in server mode it consults the session. Keeping it as a
	// function is what lets both modes share every handler, rather than each
	// handler asking which mode it is in.
	identity func(r *http.Request) (domain.User, error)
	// accounts is nil in local mode. Every authentication route checks it, so a
	// local instance cannot be talked into a login flow it has no state for.
	accounts *service.Accounts
	// oidc is nil unless single sign-on is configured.
	oidc *auth.OIDCProvider
	// hardening summarises the sandboxing that took effect, for the health
	// endpoint.
	hardening string
}

// Options are the collaborators the run mode selects at start-up.
type Options struct {
	// Identity resolves the acting user. Required.
	Identity func(*http.Request) (domain.User, error)
	// Accounts enables the authentication routes. Nil in local mode.
	Accounts *service.Accounts
	// OIDC enables the single sign-on routes. Nil unless configured.
	OIDC *auth.OIDCProvider
	// Hardening summarises what the sandbox applied, for the health endpoint.
	Hardening string
}

// New builds the HTTP server.
func New(svc *service.Service, cfg config.Config, log *slog.Logger, opts Options) (*Server, error) {
	if opts.Identity == nil {
		return nil, fmt.Errorf("an identity resolver is required")
	}
	hardeningSummary := opts.Hardening
	if hardeningSummary == "" {
		hardeningSummary = "none"
	}
	s := &Server{
		svc: svc, cfg: cfg, log: log,
		identity: opts.Identity, accounts: opts.Accounts, oidc: opts.OIDC,
		hardening: hardeningSummary,
	}

	var err error
	if s.templates, err = parseTemplates(); err != nil {
		return nil, err
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

// ServeHTTP applies the middleware chain and dispatches.
//
// The order is not arbitrary, and each position is load-bearing:
//
//	recovery         outermost, so a panic anywhere still produces a response
//	request id       established before logging, so every line correlates
//	logging          sees the final status of everything below it
//	security headers set before any handler can write a body
//	identity         resolves the actor into the context for the service layer
//	CSRF             runs after identity, because the token lives on the session
//
// Reordering identity and CSRF would be a real bug: the check needs the session
// the identity layer resolved.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := s.requireCSRF(s.mux)
	handler = s.withIdentity(handler)
	handler = s.withSecurityHeaders(handler)
	handler = s.withLogging(handler)
	handler = s.withRequestID(handler)
	handler = s.withRecovery(handler)
	handler.ServeHTTP(w, r)
}

// parseTemplates builds one template set per page.
//
// Each set contains the layout, the shared fragments and one page, so a page can
// be rendered whole or a single fragment of it can be rendered on its own for an
// HTMX swap - from the same definitions, which is what stops full-page and
// partial renders from drifting apart.
func parseTemplates() (map[string]*template.Template, error) {
	pages, err := fs.Glob(templateFS, "templates/page_*.html")
	if err != nil {
		return nil, fmt.Errorf("find page templates: %w", err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no page templates found")
	}

	sets := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		name := strings.TrimPrefix(page, "templates/")
		set, err := template.New("layout.html").Funcs(templateFuncs()).ParseFS(
			templateFS, "templates/layout.html", "templates/fragments.html", page)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		sets[name] = set
	}
	return sets, nil
}

// render writes a full page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	set, ok := s.templates[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("unknown template %q", page))
		return
	}

	// Render into a buffer first. Writing directly to the ResponseWriter would
	// commit a 200 and a half-written page if a template failed midway, leaving
	// the user with a broken screen and no error.
	var buf strings.Builder
	if err := set.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.serverError(w, r, fmt.Errorf("render %s: %w", page, err))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, buf.String())
}

// renderFragment writes one named template, for an HTMX partial swap.
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, page, fragment string, data any) {
	set, ok := s.templates[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("unknown template %q", page))
		return
	}

	var buf strings.Builder
	if err := set.ExecuteTemplate(&buf, fragment, data); err != nil {
		s.serverError(w, r, fmt.Errorf("render fragment %s/%s: %w", page, fragment, err))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, buf.String())
}

// isHTMX reports whether the request came from HTMX, and therefore expects a
// fragment rather than a whole page. Every screen still works without it, which
// is what keeps the application usable with JavaScript disabled.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// init registers the media types Go's table does not carry.
//
// http.ServeContent picks a type from the file extension, and the standard
// table has no entry for .webmanifest - so a manifest goes out as text/plain,
// which Chrome accepts with a warning and stricter clients refuse outright.
// Registering it once here covers both the /static/ file server and the
// root-path alias, rather than setting a header in two places that could drift.
func init() {
	// The type is the one the W3C specifies for a web app manifest.
	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		// Only reachable with a malformed extension, which is a literal here.
		panic("register the manifest media type: " + err.Error())
	}
}

// EntriesPerPage is how many entries the Entries screen shows at once.
//
// Fifty: a screenful somebody can scan, and small enough that the page renders
// in a few milliseconds against a database with years in it. It was a thousand,
// which took 135 ms to render and produced a page nobody read to the end of.
const EntriesPerPage = 50

// staticAsset serves one embedded file at a fixed path.
//
// Used for the root-path icons a browser asks for without being told. The name
// is a constant from the routing table and never comes from the request, so
// there is nothing here to traverse with.
func (s *Server) staticAsset(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			// Only reachable if the routing table names a file the embed
			// directive did not include, which is a build-time mistake. The
			// test in server_test.go turns it into a failing build.
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}

// staticHandler serves the embedded CSS, JavaScript and icons.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Only reachable if the embed directive and this path disagree, which is
		// a build-time mistake rather than a runtime condition.
		panic(fmt.Sprintf("static assets not embedded: %v", err))
	}

	// The route is mounted at /static/, but the embedded filesystem is rooted at
	// the static directory itself, so the prefix has to come off before lookup.
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are embedded in the binary, so they change only when the binary
		// does. A short cache keeps a page load cheap without stranding a user on
		// stale CSS after an upgrade.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}

// templateFuncs are the helpers available to every template.
//
// They are all pure formatting. Anything that needs a decision belongs in the
// service layer, so that a template cannot become a place where business rules
// hide.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// duration renders seconds as "1h 30m".
		"duration": domain.FormatDuration,
		// hours renders seconds as decimal hours, "1.50".
		"hours": domain.FormatDecimalHours,
		// clock renders an instant as "09:30" in a given location.
		"clock": func(t time.Time, loc *time.Location) string {
			if loc == nil {
				loc = time.UTC
			}
			return t.In(loc).Format("15:04")
		},
		"date":     func(t time.Time) string { return t.Format("2006-01-02") },
		"dateLong": func(t time.Time) string { return t.Format("Monday 2 January 2006") },
		"weekday":  func(t time.Time) string { return t.Format("Mon") },
		"dayNum":   func(t time.Time) string { return t.Format("2") },
		"iso":      func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		// addDays is used by the previous/next navigation links.
		"addDays": func(t time.Time, days int) time.Time { return t.AddDate(0, 0, days) },
		// nth is a bounds-safe element lookup, for the week grid where a row can
		// be shorter than the number of columns.
		//
		// Deliberately not called "index": that is the name of a Go template
		// builtin, and shadowing it silently breaks every other use - a map
		// lookup elsewhere on the page fails with "expected []int64", which
		// says nothing about the real cause.
		"nth": func(values []int64, i int) int64 {
			if i < 0 || i >= len(values) {
				return 0
			}
			return values[i]
		},
		"isToday": func(t time.Time, now time.Time) bool {
			return t.Year() == now.Year() && t.YearDay() == now.YearDay()
		},
		// barWidth caps a percentage at the width of its track. The number
		// beside the bar is the real one and is not capped - the bar is a
		// picture, and a picture 340% wide is a layout bug rather than
		// emphasis.
		"barWidth": func(percent int) int {
			if percent < 0 {
				return 0
			}
			if percent > 100 {
				return 100
			}
			return percent
		},
		// money renders minor units as a decimal string.
		"money": func(minor int64, currency string) string {
			return domain.NewMoney(minor, currency).String()
		},
		"dict": dict,
		// add and sub are for the pager's neighbouring page numbers. A template
		// cannot do arithmetic, and the alternative - precomputing "previous"
		// and "next" onto the form - puts view logic in a struct to avoid two
		// three-line functions.
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// divide is integer division, for showing a stored seconds value as
		// hours in a form field.
		"divide": func(value int64, by int64) int64 {
			if by == 0 {
				return 0
			}
			return value / by
		},
		// hoursOf renders a seconds threshold as the hours a contract states it
		// in, so what is displayed is what can be typed back.
		"hoursOf": formatHours,
		// quantity renders a thousandths quantity as a plain decimal.
		"quantity": domain.FormatQuantity,
		// weekdayNames are Monday to Sunday, for a routine's day picker.
		"weekdayNames": domain.WeekdayNames,
		// hasWeekday reports whether a routine covers a day.
		"hasWeekday": func(days []int, day int) bool {
			for _, candidate := range days {
				if candidate == day {
					return true
				}
			}
			return false
		},
		// tagList renders an entry's tags back into the form the field accepts,
		// so what is displayed can be typed back.
		"tagList": domain.FormatTagList,
		// zeroRules is an empty rule set, for a form that is adding rather than
		// editing. A template cannot construct a struct, and rendering the form
		// against nil would need every field guarded.
		"zeroRules": func() domain.RateRules { return domain.RateRules{} },
		// reportStatuses are the marks used in the approval grid, for its key.
		"reportStatuses": service.ApprovalStatuses,
		// entryKinds are work, overtime and travel.
		"entryKinds": domain.EntryKinds,
		// exportFormats are every encoding the export package can write, so the
		// buttons on the Entries screen cannot drift out of step with what the
		// router accepts.
		"exportFormats": func() []export.Format { return export.Formats },
		// The presentation choices, from the same lists the validator accepts,
		// so a form cannot offer an option that would be silently discarded.
		"navPositions": service.NavPositions,
		"clockFormats": service.ClockFormats,
		"dateFormats":  service.DateFormats,
		"dayOverflows": service.DayOverflows,
		// humanBytes renders a file size the way a person reads one.
		"humanBytes": humanBytes,
		// previewKind says how an attachment can be shown, decided from its
		// recorded type and name. It reads nothing, so a page listing twenty
		// receipts does not open twenty files to lay itself out.
		"previewKind": func(a domain.Attachment) string {
			return string(service.Previewable(a))
		},
		// colours are the palette keys an entity may carry. Stored as keys
		// rather than values so each theme maps them to something legible on
		// its own background (docs/adr/0011-theming-via-css-custom-properties.md).
		"colours": func() []string {
			return []string{"blue", "green", "amber", "red", "purple", "teal", "slate"}
		},
	}
}

// humanBytes formats a byte count.
//
// Powers of 1024 with the conventional short suffixes, which is what a file
// listing shows everywhere else on the machine; being pedantic about KiB here
// would only look like a typo.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// dict builds a map inside a template, so a fragment can be given several values.
// Templates cannot construct maps, and passing a single struct per fragment would
// mean a named type for every partial.
func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict needs an even number of arguments")
	}
	m := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		m[key] = values[i+1]
	}
	return m, nil
}
