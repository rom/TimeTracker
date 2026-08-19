package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Accessibility tests.
//
// These check the structural properties that automated testing can actually
// settle: landmarks, labels, the lang attribute, contrast ratios. They do not
// and cannot replace a manual pass with a screen reader, which docs/TEST.md
// lists as a release check.

// TestSkipLinkIsFirst: someone navigating by keyboard should not have to tab
// through the whole navigation on every page to reach the content.
func TestSkipLinkIsFirst(t *testing.T) {
	srv, _ := newTestServer(t)
	body := get(t, srv, "/today").Body.String()

	skip := strings.Index(body, `class="skip-link"`)
	nav := strings.Index(body, `class="mainnav"`)

	if skip < 0 {
		t.Fatal("there is no skip link")
	}
	if nav > 0 && skip > nav {
		t.Error("the skip link comes after the navigation, so it saves nobody anything")
	}
	if !strings.Contains(body, `href="#main"`) {
		t.Error("the skip link does not point at the main landmark")
	}
	if !strings.Contains(body, `id="main"`) {
		t.Error("there is no element with id=main for the skip link to reach")
	}
}

// TestLandmarksArePresent: a screen reader user navigates by landmark, and a
// page of anonymous <div>s gives them nothing to navigate by.
func TestLandmarksArePresent(t *testing.T) {
	srv, _ := newTestServer(t)
	body := get(t, srv, "/today").Body.String()

	for _, landmark := range []string{"<header", "<nav", "<main", "<footer"} {
		if !strings.Contains(body, landmark) {
			t.Errorf("no %s landmark", landmark)
		}
	}
	// A nav element with no label is announced as just "navigation", which is
	// unhelpful once there is more than one.
	if !strings.Contains(body, `aria-label="Main"`) {
		t.Error("the main navigation has no accessible name")
	}
}

// TestCurrentPageIsMarked: the highlight colour cannot be perceived by a screen
// reader, so the current page must be marked semantically as well.
func TestCurrentPageIsMarked(t *testing.T) {
	srv, _ := newTestServer(t)

	body := get(t, srv, "/week").Body.String()
	if !strings.Contains(body, `aria-current="page"`) {
		t.Error("the current page is not marked with aria-current")
	}
	// And it must be on the right link.
	weekLink := strings.Index(body, `href="/week"`)
	current := strings.Index(body, `aria-current="page"`)
	if weekLink < 0 || current < weekLink || current-weekLink > 80 {
		t.Error("aria-current is not on the link for the current page")
	}
}

// TestFormControlsHaveNames: an input a screen reader announces as "edit text"
// and nothing else is unusable.
func TestFormControlsHaveNames(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, path := range []string{"/today", "/entries", "/admin"} {
		body := get(t, srv, path).Body.String()

		// Every input and select must carry an aria-label, be wrapped in a
		// label, or be a control that needs no name (hidden, checkbox inside a
		// label, submit).
		controls := regexp.MustCompile(`<(input|select)[^>]*>`).FindAllString(body, -1)
		for _, control := range controls {
			switch {
			case strings.Contains(control, `type="hidden"`),
				strings.Contains(control, "aria-label"),
				strings.Contains(control, `type="checkbox"`),
				strings.Contains(control, `type="submit"`):
				continue
			}
			// Anything left must be inside a <label>, which we approximate by
			// checking that a label opens shortly before it.
			index := strings.Index(body, control)
			window := body[max(0, index-200):index]
			if !strings.Contains(window, "<label") {
				t.Errorf("%s: a control has no accessible name: %s", path, control)
			}
		}
	}
}

// TestTotalsAreAnnounced: the totals change under the user when a timer is
// stopped elsewhere on the page, and a screen reader user would otherwise have
// no way to know.
func TestTotalsAreAnnounced(t *testing.T) {
	srv, _ := newTestServer(t)
	body := get(t, srv, "/today").Body.String()

	totals := strings.Index(body, `class="totals"`)
	if totals < 0 {
		t.Fatal("no totals block")
	}
	block := body[totals:min(len(body), totals+200)]
	if !strings.Contains(block, "aria-live") {
		t.Error("the totals are not announced when they change")
	}
}

// TestColourIsNotTheOnlyCarrier: status must be readable without perceiving hue,
// which matters for the colour-blind reader as much as the screen-reader user.
func TestColourIsNotTheOnlyCarrier(t *testing.T) {
	srv, _ := newTestServer(t)

	post(t, srv, "/customers", url.Values{"name": {"Acme"}, "currency": {"SEK"}})
	post(t, srv, "/projects", url.Values{"customer_id": {"1"}, "name": {"P"}, "billable": {"on"}})
	post(t, srv, "/assignments", url.Values{"project_id": {"1"}, "name": {"A"}, "billable": {"on"}})
	post(t, srv, "/timers/start", url.Values{"assignment_id": {"1"}})

	body := get(t, srv, "/today").Body.String()

	// The billable marker is a currency glyph; it must be accompanied by text
	// for anyone who cannot see it.
	if strings.Contains(body, `<span aria-hidden="true">€</span>`) &&
		!strings.Contains(body, `class="visually-hidden">Billable`) {
		t.Error("the billable marker has no text alternative")
	}
	// Colour chips are decorative and must be hidden from assistive technology
	// rather than announced as meaningless characters.
	if strings.Contains(body, `class="chip"`) && !strings.Contains(body, `aria-hidden="true"`) {
		t.Error("decorative colour chips are not hidden from assistive technology")
	}
}

// TestHighContrastThemeMeetsWCAG is the objective part of ASR-009.
//
// It computes real contrast ratios from the stylesheet rather than trusting that
// the colours look right: 4.5:1 for body text and 3:1 for large text and UI
// borders, per WCAG 2.1 AA.
func TestHighContrastThemeMeetsWCAG(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	block := themeBlock(string(css), "contrast")
	if block == "" {
		t.Fatal("no high-contrast theme block")
	}

	tokens := parseTokens(block)

	// The pairs that actually appear together in the interface.
	pairs := []struct {
		foreground, background string
		minimum                float64
		what                   string
	}{
		{"--text", "--surface", 4.5, "body text on the page"},
		{"--text", "--surface-raised", 4.5, "body text on a card"},
		{"--text-muted", "--surface", 4.5, "muted text on the page"},
		{"--text-muted", "--surface-raised", 4.5, "muted text on a card"},
		{"--accent-text", "--accent", 4.5, "text on a primary button"},
		{"--accent", "--surface", 4.5, "links on the page"},
		{"--danger", "--surface", 4.5, "error text"},
		{"--border", "--surface", 3.0, "borders against the page"},
	}

	for _, pair := range pairs {
		fg, okFG := tokens[pair.foreground]
		bg, okBG := tokens[pair.background]
		if !okFG || !okBG {
			t.Errorf("the contrast theme does not define %s or %s", pair.foreground, pair.background)
			continue
		}
		ratio := contrastRatio(fg, bg)
		if ratio < pair.minimum {
			t.Errorf("%s: contrast %.2f:1 between %s and %s, need %.1f:1",
				pair.what, ratio, pair.foreground, pair.background, pair.minimum)
		}
	}
}

// parseTokens pulls "--name: #rrggbb;" declarations out of a CSS block.
func parseTokens(block string) map[string]string {
	tokens := map[string]string{}
	pattern := regexp.MustCompile(`(--[a-z-]+)\s*:\s*(#[0-9a-fA-F]{6})`)
	for _, match := range pattern.FindAllStringSubmatch(block, -1) {
		tokens[match[1]] = match[2]
	}
	return tokens
}

// contrastRatio computes the WCAG contrast ratio between two hex colours.
func contrastRatio(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance implements the WCAG 2.1 definition.
func relativeLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0
	}
	channel := func(offset int) float64 {
		value, err := strconv.ParseInt(hex[offset:offset+2], 16, 32)
		if err != nil {
			return 0
		}
		c := float64(value) / 255.0
		if c <= 0.03928 {
			return c / 12.92
		}
		// The WCAG formula uses a 2.4 gamma above the linear segment.
		return pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(0) + 0.7152*channel(2) + 0.0722*channel(4)
}

// pow is math.Pow, kept local so the test file needs no import for one call.
func pow(base, exponent float64) float64 {
	result := 1.0
	// A simple exponential/logarithm pair is enough here and avoids pulling in
	// math for a single use; accuracy well beyond what a contrast threshold
	// needs.
	return expApprox(exponent * lnApprox(base) * 1.0 / result)
}

func lnApprox(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// ln(x) via the arctanh series, which converges quickly for the [0,1] range
	// these channel values occupy.
	z := (x - 1) / (x + 1)
	z2 := z * z
	sum, term := 0.0, z
	for n := 1; n < 40; n += 2 {
		sum += term / float64(n)
		term *= z2
	}
	return 2 * sum
}

func expApprox(x float64) float64 {
	sum, term := 1.0, 1.0
	for n := 1; n < 40; n++ {
		term *= x / float64(n)
		sum += term
	}
	return sum
}

// TestHelpIsAvailableWithoutJavaScript: the help button is a real link, so a
// browser with no script simply navigates to it.
func TestHelpIsAvailableWithoutJavaScript(t *testing.T) {
	srv, _ := newTestServer(t)

	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `href="/help/today"`) {
		t.Error("the help control is not a real link")
	}

	help := get(t, srv, "/help/today")
	if help.Code != http.StatusOK {
		t.Fatalf("GET /help/today = %d", help.Code)
	}
	helpBody := help.Body.String()
	if !strings.Contains(helpBody, "<html") {
		t.Error("navigating to help did not return a whole page")
	}
	if !strings.Contains(helpBody, "quick add") && !strings.Contains(helpBody, "Quick add") {
		t.Error("the day view's help does not mention quick add")
	}
}

// TestUnknownHelpScreenFallsBack: someone who has found their way to a help URL
// is asking for help, and an error page is a poor answer.
func TestUnknownHelpScreenFallsBack(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := get(t, srv, "/help/nonsense")
	if rec.Code != http.StatusOK {
		t.Errorf("GET /help/nonsense = %d, want a fallback rather than an error", rec.Code)
	}
}

// TestHelpMarkupIsEscaped: help text is written by translators, and the renderer
// marks its output as trusted HTML, so the escaping has to be right.
func TestHelpMarkupIsEscaped(t *testing.T) {
	rendered := string(renderHelpBody("a <script>alert(1)</script> and **bold** and `code`"))

	if strings.Contains(rendered, "<script>") {
		t.Error("script markup was not escaped")
	}
	if !strings.Contains(rendered, "&lt;script&gt;") {
		t.Error("the angle brackets were not escaped")
	}
	if !strings.Contains(rendered, "<strong>bold</strong>") {
		t.Error("bold markup was not applied")
	}
	if !strings.Contains(rendered, "<code>code</code>") {
		t.Error("code markup was not applied")
	}
}

// TestEntityTintsKeepTextReadable is the objective check on colouring the whole
// line rather than a dot.
//
// A row tinted toward the customer's colour is easier to scan and harder to
// read: every percent of tint is contrast taken away from the text on top of
// it. So the mix the stylesheet performs is recomputed here, for every entity
// colour in every theme, and the body text is asserted to still clear WCAG AA.
//
// This is the test that lets the tint exist at all. Without it, "does 10% of
// amber over a sand background still leave readable text" is a question nobody
// can answer for forty-nine combinations by looking.
func TestEntityTintsKeepTextReadable(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	stylesheet := string(css)

	// Every mix percentage the stylesheet uses, not one: a timeline block is a
	// small target and legitimately wants a stronger tint than a table row, so
	// the rule is "each level stays readable" rather than "one level
	// everywhere".
	percents := entityTintPercents(t, stylesheet)
	if len(percents) == 0 {
		t.Fatal("no entity tint found in the stylesheet")
	}

	entities := []string{
		"--entity-blue", "--entity-green", "--entity-amber",
		"--entity-red", "--entity-purple", "--entity-teal", "--entity-slate",
	}

	for _, theme := range availableThemes {
		block := themeBlock(stylesheet, theme)
		if block == "" {
			// The default theme is declared on :root rather than in a themed
			// block, and is covered by the light entry below.
			continue
		}
		tokens := parseTokens(block)
		surface, ok := tokens["--surface-raised"]
		if !ok {
			continue
		}
		text, ok := tokens["--text"]
		if !ok {
			continue
		}

		for _, percent := range percents {
			for _, entity := range entities {
				colour, ok := tokens[entity]
				if !ok {
					continue
				}
				tinted := mixColours(colour, surface, percent)
				if ratio := contrastRatio(text, tinted); ratio < 4.5 {
					t.Errorf("theme %s: body text on a surface tinted %d%% with %s is %.2f:1, "+
						"need 4.5:1 (%s over %s gives %s)",
						theme, percent, entity, ratio, colour, surface, tinted)
				}
			}
		}
	}
}

// entityTintPercents lists every percentage the stylesheet mixes entity colours
// at, de-duplicated.
func entityTintPercents(t *testing.T, css string) []int {
	t.Helper()
	pattern := regexp.MustCompile(`color-mix\(in srgb, var\(--entity-[a-z]+\) (\d+)%`)

	seen := map[int]bool{}
	var percents []int
	for _, match := range pattern.FindAllStringSubmatch(css, -1) {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("unreadable tint percentage %q", match[1])
		}
		if !seen[value] {
			seen[value] = true
			percents = append(percents, value)
		}
	}
	return percents
}

// mixColours blends percent of a into b, the way CSS color-mix does in sRGB.
func mixColours(a, b string, percent int) string {
	ar, ag, ab := hexToRGB(a)
	br, bg, bb := hexToRGB(b)

	blend := func(x, y int) int {
		// Rounded rather than truncated, matching what a browser produces.
		return (x*percent + y*(100-percent) + 50) / 100
	}
	return fmt.Sprintf("#%02x%02x%02x", blend(ar, br), blend(ag, bg), blend(ab, bb))
}

// hexToRGB splits a #rrggbb colour into its channels.
func hexToRGB(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0
	}
	channel := func(offset int) int {
		value, err := strconv.ParseInt(hex[offset:offset+2], 16, 32)
		if err != nil {
			return 0
		}
		return int(value)
	}
	return channel(0), channel(2), channel(4)
}
