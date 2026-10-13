package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Every screen, as a client.
//
// A client is the one role that signs in and may change nothing at all. The
// navigation has never offered them more than the entries listing and the
// guide, and its comment says exactly why: "offering a link whose only outcome
// is a refusal is a worse answer than not offering it". The same comment says
// the data behind those screens is narrowed regardless, "this is presentation,
// and presentation is never the enforcement".
//
// For six screens it was the only thing there. Typing /routines, /move,
// /import, /calendar or /tags got a live page of forms whose every submission
// is refused - and two of them answered with somebody else's data: /tags listed
// the whole instance's vocabulary, which carries other customers' work in the
// names, and the correction and catalogue edit forms arrived with an
// assignment's hourly rate and a project's budget typed into their fields,
// because a form that edits a record was authorised as a read of it.
//
// So this walks the route table and requires a decided answer for every GET.

// clientScreens are the screens that are legitimately a client's, with why.
//
// Everything else must redirect them to the listing or refuse them. A route
// absent from both this map and the sweep's own expectations fails the
// completeness check below, so a screen added later is decided rather than
// discovered.
var clientScreens = map[string]string{
	"GET /{$}":                              "the front door, which redirects them to the listing",
	"GET /entries":                          "the work done for them: the one screen the role exists for",
	"GET /export/preview":                   "what their own download will contain",
	"GET /export/{format}":                  "their own download, narrowed by the projection",
	"GET /export/{format}/view":             "the same document, shown rather than saved",
	"GET /guide":                            "documentation, which is the same for everybody",
	"GET /guide/{topic}":                    "documentation",
	"GET /help/{screen}":                    "the help for a screen they are on",
	"GET /healthz":                          "the health check, which has no identity at all",
	"GET /login":                            "the sign-in form",
	"GET /auth/oidc/start":                  "single sign-on",
	"GET /auth/oidc/callback":               "single sign-on",
	"GET /static/":                          "stylesheets and scripts",
	"GET /favicon.ico":                      "an icon",
	"GET /apple-touch-icon.png":             "an icon",
	"GET /apple-touch-icon-precomposed.png": "an icon",
	"GET /site.webmanifest":                 "the web manifest",
}

// clientProbe is the request that exercises a route pattern, where the pattern
// needs an id or a parameter to get past its own parsing.
var clientProbe = map[string]string{
	"GET /{$}":                      "/",
	"GET /export/{format}":          "/export/csv",
	"GET /export/{format}/view":     "/export/pdf/view",
	"GET /guide/{topic}":            "/guide/record",
	"GET /help/{screen}":            "/help/today",
	"GET /attachments/{id}":         "/attachments/1",
	"GET /attachments/{id}/preview": "/attachments/1/preview",
	"GET /entries/{id}/edit":        "/entries/1/edit",
	"GET /customers/{id}/edit":      "/customers/1/edit",
	"GET /projects/{id}/edit":       "/projects/1/edit",
	"GET /assignments/{id}/edit":    "/assignments/1/edit",
	"GET /terms/{scope}/{id}":       "/terms/customer/1",
	"GET /backup/preview":           "/backup/preview?file=anything.zip",
	"GET /static/":                  "/static/css/app.css",
}

// TestEveryScreenHasADecidedAnswerForAClient.
func TestEveryScreenHasADecidedAnswerForAClient(t *testing.T) {
	_, reviewer := instanceWithAClient(t)

	checked := 0
	for _, route := range registeredRoutes(t) {
		if !strings.HasPrefix(route, "GET ") {
			// A write a client attempts is refused by the service, which is the
			// subject of the client tests in the service package rather than of
			// this one.
			continue
		}
		path := clientProbe[route]
		if path == "" {
			path = strings.TrimPrefix(route, "GET ")
		}
		checked++

		t.Run(route, func(t *testing.T) {
			rec := reviewer.get(t, path)
			_, theirs := clientScreens[route]

			switch {
			case theirs:
				// Their own screen: it must answer, and 404 is allowed because
				// several of these depend on a record that may not exist in
				// this fixture - an attachment nobody uploaded.
				if rec.Code >= 500 {
					t.Errorf("GET %s = %d for a client; this screen is theirs:\n%s",
						path, rec.Code, truncate(rec.Body.String()))
				}
				if rec.Code == http.StatusForbidden {
					t.Errorf("GET %s = 403 for a client, but clientScreens says "+
						"it is theirs: %s", path, clientScreens[route])
				}
			default:
				// Not theirs: sent to the listing, or refused. Never rendered.
				if rec.Code == http.StatusOK {
					t.Errorf("GET %s = 200 for a client, and it is not in "+
						"clientScreens. Decide it: send them to their listing "+
						"with sentToTheirOwnListing if the screen is about "+
						"recording time, refuse it if it is administration, or "+
						"add it to clientScreens with the reason it is theirs.",
						path)
				}
				if rec.Code == http.StatusSeeOther {
					if target := rec.Header().Get("Location"); !strings.HasPrefix(target, "/entries") {
						t.Errorf("GET %s sent a client to %q, want their listing",
							path, target)
					}
				}
			}
		})
	}

	if checked < 25 {
		t.Errorf("only %d GET routes were swept; this test is not reading the "+
			"route table", checked)
	}

	// The map is only useful while every name in it is a real route: a stale
	// entry would quietly excuse a screen that no longer exists, or misspell one
	// that does and leave it unswept.
	registered := map[string]bool{}
	for _, route := range registeredRoutes(t) {
		registered[route] = true
	}
	for route, why := range clientScreens {
		if !registered[route] {
			t.Errorf("clientScreens names %s (%q), which is not a registered "+
				"route", route, why)
		}
	}
	for route := range clientProbe {
		if !registered[route] {
			t.Errorf("clientProbe names %s, which is not a registered route", route)
		}
	}
}

// TestAClientSeesNoControlTheyCannotUse.
//
// The screens that are theirs are read-only screens, so a form on one is a
// button that answers 403. The entries listing has hidden its row controls from
// a client for a long time; the expenses listing offered a recording form, a
// receipt upload and a delete on every row.
func TestAClientSeesNoControlTheyCannotUse(t *testing.T) {
	_, reviewer := instanceWithAClient(t)

	// Every form on a screen that is theirs, except the ones a reader needs:
	// the filter and search forms are GETs, and signing out is a POST they may
	// certainly make.
	postForm := regexp.MustCompile(`<form[^>]*method="post"[^>]*action="([^"]*)"`)
	allowed := map[string]bool{"/logout": true}

	for _, path := range []string{"/entries", "/export/preview"} {
		body := reviewer.get(t, path).Body.String()
		for _, match := range postForm.FindAllStringSubmatch(body, -1) {
			action := match[1]
			if allowed[action] || strings.HasPrefix(action, "/preferences/") {
				// Their own preferences: language, theme, and the clock.
				continue
			}
			t.Errorf("%s offers a client a form posting to %s, which the service "+
				"refuses - a control that answers 403 makes a read-only report "+
				"look broken", path, action)
		}
	}
}

// TestTheTagVocabularyIsNotAClientsToRead.
//
// A tag is internal shorthand, and the names carry other customers' work in
// them. The projection strips tags from a client's entries on purpose, so
// handing them the instance's whole vocabulary said the opposite - through the
// tag screen, and through the datalist on the correction form.
func TestTheTagVocabularyIsNotAClientsToRead(t *testing.T) {
	_, reviewer := instanceWithAClient(t)

	for _, path := range []string{"/tags", "/entries", "/entries/1/edit",
		"/export/preview", "/expenses"} {
		body := reviewer.get(t, path).Body.String()
		for _, tag := range []string{"globex-outage", "acme-internal"} {
			if strings.Contains(body, tag) {
				t.Errorf("GET %s showed a client the tag %q", path, tag)
			}
		}
	}
}

// instanceWithAClient is a server-mode instance holding two customers' work,
// signed in as a client of the first.
//
// Two customers on purpose: a leak that shows a client their *own* customer's
// data is a different question from one that shows them somebody else's, and
// only the second is visible with one customer in the fixture.
func instanceWithAClient(t *testing.T) (*Server, *client) {
	t.Helper()

	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)

	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"},
		"rate": {"1250.00"}})
	admin.post(t, "/customers", url.Values{"name": {"Globex"}, "currency": {"EUR"},
		"rate": {"1900.00"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"},
		"billable": {"on"}, "currency": {"EUR"}, "rate": {"1375.00"}, "budget_hours": {"40"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"2"}, "name": {"Rollout"},
		"billable": {"on"}, "currency": {"EUR"}})
	admin.post(t, "/assignments", url.Values{"project_id": {"1"}, "name": {"Development"},
		"billable": {"on"}, "rate": {"1480.00"}})
	admin.post(t, "/assignments", url.Values{"project_id": {"2"}, "name": {"Support"},
		"billable": {"on"}})

	// Work for each, tagged: the tag names are the thing a client must not see.
	admin.post(t, "/entries", url.Values{"assignment_id": {"1"}, "date": {"2026-03-16"},
		"start": {"09:00"}, "duration": {"2h"}, "tags": {"acme-internal"},
		"note": {"a note the client must not see"}})
	admin.post(t, "/entries", url.Values{"assignment_id": {"2"}, "date": {"2026-03-16"},
		"start": {"13:00"}, "duration": {"3h"}, "tags": {"globex-outage"}})
	admin.post(t, "/expenses", url.Values{"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"category": {"travel"}, "description": {"Taxi"}, "amount": {"100"},
		"billable": {"on"}})

	admin.post(t, "/users", url.Values{
		"display_name": {"Acme's Reviewer"}, "email": {"reviewer@example.com"},
		"password": {"a-long-enough-password"}, "role": {"client"},
		"client_customer_id": {"1"},
	})

	reviewer := signInAsReadOnly(t, srv, accounts,
		"reviewer@example.com", "a-long-enough-password")
	return srv, reviewer
}

// TestAnEditFormIsNotAWayToReadWhatItEdits.
//
// The four edit forms loaded their record with a view check while the save they
// submit to requires update or manage. So the form arrived filled in, from the
// unnarrowed record, for anybody who could merely read it: a client got an
// assignment's hourly rate and a project's budget typed into fields, which are
// exactly the figures the projection removes from the same records elsewhere.
//
// A form cannot be narrowed instead - it saves its fields back, so narrowing it
// would write the narrowing into the record. The check is what has to change.
func TestAnEditFormIsNotAWayToReadWhatItEdits(t *testing.T) {
	_, reviewer := instanceWithAClient(t)

	// Their own customer's records, which is what makes this the sharper case:
	// a client may read the customer, the project and the assignment by name.
	// What they may not read is the commercial terms attached to them.
	for _, form := range []struct {
		path    string
		secrets []string
	}{
		{"/customers/1/edit", []string{"1250"}},
		{"/projects/1/edit", []string{"1375", "40"}},
		{"/assignments/1/edit", []string{"1480"}},
		{"/entries/1/edit", []string{"a note the client must not see", "acme-internal"}},
	} {
		t.Run(form.path, func(t *testing.T) {
			rec := reviewer.get(t, form.path)
			if rec.Code == http.StatusOK {
				t.Errorf("GET %s = 200 for a client", form.path)
			}
			body := rec.Body.String()
			for _, secret := range form.secrets {
				if strings.Contains(body, secret) {
					t.Errorf("GET %s showed a client %q", form.path, secret)
				}
			}
		})
	}
}

// TestTheInstanceSettingsAreNotEverybodysToRead.
//
// The settings screen carries the instance's default currency and hourly rate,
// the timer ceiling, the idle threshold and whether a backup password is set.
// Every control on it posts to UpdateSettings, which requires administration -
// and the screen asked nothing, so a member read the default rate off a form
// they could not submit. ADR-0008 gives a member "own rates only".
func TestTheInstanceSettingsAreNotEverybodysToRead(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)

	admin.post(t, "/settings", url.Values{"default_currency": {"EUR"},
		"default_rate": {"1950.00"}, "week_start": {"1"}})
	admin.post(t, "/users", url.Values{
		"display_name": {"M"}, "email": {"m@example.com"},
		"password": {"a-long-enough-password"}, "role": {"member"},
	})
	member := signInAs(t, srv, accounts, "m@example.com", "a-long-enough-password")

	rec := member.get(t, "/settings")
	if rec.Code == http.StatusOK {
		t.Errorf("a member opened the instance settings: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "1950") {
		t.Error("a member read the instance's default hourly rate")
	}

	// The administrator still reads and saves it, which is the half worth
	// asserting: a gate that refuses everybody is not a fix.
	if rec := admin.get(t, "/settings"); rec.Code != http.StatusOK {
		t.Errorf("the administrator can no longer open the settings: %d", rec.Code)
	}
	if !strings.Contains(admin.get(t, "/settings").Body.String(), "1950") {
		t.Error("the settings screen no longer shows the administrator the rate " +
			"it is for editing")
	}

	// And their own display preferences are untouched. Those are not on this
	// screen at all: the language and theme pickers sit in the topbar of every
	// screen and post to /preferences/*, which is why gating the instance
	// settings takes nothing away from somebody who only wanted dark mode.
	for _, path := range []string{"/entries", "/today"} {
		body := member.get(t, path).Body.String()
		for _, control := range []string{`id="language-select"`, `id="theme-select"`} {
			if !strings.Contains(body, control) {
				t.Errorf("a member lost %s on %s", control, path)
			}
		}
	}
}

// TestAReceiptIsNotAClientsToFetch.
//
// The projection drops the attachment count from a client's entries, and says
// why as a statement of fact: "a client cannot fetch an attachment, and a count
// of files they cannot see is an invitation to ask about them". They could. The
// check asked whether the actor may view the owning record, which a client may
// for their own customer's expense, so a receipt came back in full to anybody
// who guessed its id - and they are never shown one to click, which is the only
// reason it went unnoticed.
//
// A receipt is also a document nobody composed for a client: a hotel bill with
// a colleague's name on it, a taxi receipt from somebody's home address.
func TestAReceiptIsNotAClientsToFetch(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)

	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"},
		"billable": {"on"}})
	admin.post(t, "/expenses", url.Values{"project_id": {"1"}, "spent_on": {"2026-03-16"},
		"category": {"travel"}, "description": {"Taxi"}, "amount": {"100"},
		"billable": {"on"}})

	const secret = "a receipt nobody outside the company composed"
	if rec := uploadAs(t, srv, admin, "expense", 1, "receipt.txt", []byte(secret)); rec.Code >= 400 {
		t.Fatalf("upload a receipt = %d: %s", rec.Code, truncate(rec.Body.String()))
	}

	admin.post(t, "/users", url.Values{
		"display_name": {"Acme's Reviewer"}, "email": {"reviewer@example.com"},
		"password": {"a-long-enough-password"}, "role": {"client"},
		"client_customer_id": {"1"},
	})
	reviewer := signInAsReadOnly(t, srv, accounts,
		"reviewer@example.com", "a-long-enough-password")

	for _, path := range []string{"/attachments/1", "/attachments/1/preview"} {
		rec := reviewer.get(t, path)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200 for a client", path)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("GET %s handed a client the receipt's contents", path)
		}
	}

	// And the administrator still has it, which is the half worth asserting.
	if rec := admin.get(t, "/attachments/1"); rec.Code != http.StatusOK {
		t.Errorf("the administrator can no longer open the receipt: %d", rec.Code)
	}
}

// TestTheCatalogueScreenShowsAMemberNoMoneyAndNoControls.
//
// ADR-0008's role table gives a member "own time and expenses; reads the
// projects they are a member of; sees own rates only" - and the catalogue screen
// showed them every customer's negotiated rate and every project's budget,
// beside create, edit and archive forms that each answer 403. The only narrowing
// that existed was the client's, and nothing applied it to anybody else.
//
// The screen stays theirs: reading the catalogue is in the role, and it is where
// a favourite is marked - which the service calls a working preference rather
// than administration, and authorises with a plain view.
func TestTheCatalogueScreenShowsAMemberNoMoneyAndNoControls(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)

	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"},
		"rate": {"1250.00"}})
	admin.post(t, "/projects", url.Values{"customer_id": {"1"}, "name": {"Migration"},
		"billable": {"on"}, "currency": {"EUR"}, "rate": {"1375.00"},
		"budget_hours": {"40"}})
	admin.post(t, "/assignments", url.Values{"project_id": {"1"}, "name": {"Development"},
		"billable": {"on"}, "rate": {"1480.00"}})
	admin.post(t, "/users", url.Values{"display_name": {"Member"},
		"email": {"m@example.com"}, "password": {"a-long-enough-password"},
		"role": {"member"}})
	admin.post(t, "/members", url.Values{"project_id": {"1"}, "user_id": {"2"}})

	member := signInAs(t, srv, accounts, "m@example.com", "a-long-enough-password")

	rec := member.get(t, "/admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("a member's GET /admin = %d; reading the catalogue is in their "+
			"role\n%s", rec.Code, truncate(rec.Body.String()))
	}
	body := rec.Body.String()

	// They are on the project, so the catalogue is not empty - otherwise this
	// test would pass on a screen with no rows at all.
	for _, name := range []string{"Acme", "Migration", "Development"} {
		if !strings.Contains(body, name) {
			t.Errorf("the catalogue does not list %q, so this test is reading an "+
				"empty screen", name)
		}
	}

	// No commercial figures, in any of the three tables.
	for _, figure := range []string{"1250", "1375", "1480"} {
		if strings.Contains(body, figure) {
			t.Errorf("a member reads %q off the catalogue screen", figure)
		}
	}

	// No control they cannot use - and the favourite, which they can.
	for _, refused := range []string{
		`action="/customers"`, `action="/projects"`, `action="/assignments"`,
		"/customers/1/edit", "/projects/1/edit", "/assignments/1/edit",
		"/customers/1/archive", "/projects/1/archive", "/assignments/1/archive",
		`href="/settings"`, `href="/backup"`, `href="/import"`,
	} {
		if strings.Contains(body, refused) {
			t.Errorf("the catalogue screen offers a member %s, which the service "+
				"refuses", refused)
		}
	}
	if !strings.Contains(body, "/assignments/1/favourite") {
		t.Error("the favourite control is gone; it is the one thing on this " +
			"screen a member is meant to do")
	}

	// And it works, rather than merely being rendered.
	if rec := member.post(t, "/assignments/1/favourite", url.Values{}); rec.Code >= 400 {
		t.Errorf("a member could not mark a favourite: %d\n%s", rec.Code,
			truncate(rec.Body.String()))
	}
	if !strings.Contains(member.get(t, "/admin").Body.String(), "★") {
		t.Error("the favourite did not take")
	}

	// The administrator's own view is unchanged: they manage the catalogue and
	// they price it.
	adminBody := admin.get(t, "/admin").Body.String()
	for _, figure := range []string{"1250", "1375"} {
		if !strings.Contains(adminBody, figure) {
			t.Errorf("the administrator can no longer see %q on the catalogue",
				figure)
		}
	}
	if !strings.Contains(adminBody, `action="/customers"`) {
		t.Error("the administrator lost the create-customer form")
	}
}

// TestTheNavigationOffersNoLinkItsOwnerWillBeRefused.
//
// The navigation's comment states the rule and the caveat: "offering a link
// whose only outcome is a refusal is a worse answer than not offering it", and
// "this is presentation, and presentation is never the enforcement". The rule
// had only ever been applied to a client - so a member was offered the tag
// editor and the user list, both of which refuse them.
//
// Walked rather than listed, so a link added to the layout is covered without
// anybody remembering to add it here.
func TestTheNavigationOffersNoLinkItsOwnerWillBeRefused(t *testing.T) {
	srv, accounts := newServerModeTestServer(t)
	admin := newClient(t, srv)
	admin.post(t, "/customers", url.Values{"name": {"Acme"}, "currency": {"EUR"}})
	admin.post(t, "/users", url.Values{"display_name": {"Member"},
		"email": {"m@example.com"}, "password": {"a-long-enough-password"},
		"role": {"member"}})
	admin.post(t, "/users", url.Values{"display_name": {"Reviewer"},
		"email": {"r@example.com"}, "password": {"a-long-enough-password"},
		"role": {"client"}, "client_customer_id": {"1"}})

	href := regexp.MustCompile(`<nav class="mainnav"[^>]*>(.*?)</nav>`)
	link := regexp.MustCompile(`href="(/[^"]*)"`)

	for _, who := range []struct {
		role string
		as   *client
	}{
		{"an administrator", admin},
		{"a member", signInAs(t, srv, accounts, "m@example.com", "a-long-enough-password")},
		{"a client", signInAsReadOnly(t, srv, accounts, "r@example.com", "a-long-enough-password")},
	} {
		t.Run(who.role, func(t *testing.T) {
			// The entries listing is the one screen every role can open, so it
			// is where the navigation is read from.
			body := who.as.get(t, "/entries").Body.String()
			nav := href.FindStringSubmatch(strings.ReplaceAll(body, "\n", " "))
			if nav == nil {
				t.Fatalf("%s gets no main navigation", who.role)
			}
			links := link.FindAllStringSubmatch(nav[1], -1)
			if len(links) == 0 {
				t.Fatalf("%s is offered no links at all", who.role)
			}
			for _, match := range links {
				rec := who.as.get(t, match[1])
				switch rec.Code {
				case http.StatusOK, http.StatusSeeOther:
				default:
					t.Errorf("%s is offered %s in the navigation and gets %d from "+
						"it", who.role, match[1], rec.Code)
				}
			}
		})
	}
}
