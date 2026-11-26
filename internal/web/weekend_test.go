package web

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/domain"
)

// The weekend, as the grids draw it.
//
// Three things have to line up for the raster to appear: the cells have to
// carry the class, the stylesheet has to have a rule for it, and the rule has
// to survive the cascade. The first version of this failed the third - the
// weekend block sat above the month grid and lost every specificity tie - and
// looked completely correct in the markup while doing nothing on the screen.
// So these tests check the markup, and the stylesheet tests below check that
// the rules exist and are not overridden.

// TestTheMonthGridMarksItsWeekends.
func TestTheMonthGridMarksItsWeekends(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	body := get(t, srv, "/month?month=2026-03").Body.String()

	// March 2026 starts on a Sunday. Every Saturday and Sunday in the grid,
	// including the ones from February and April that pad the first and last
	// rows: a raster that stopped at the month's edge would read as a shaded
	// block rather than as two columns.
	for _, row := range monthRows(t, body) {
		cells := regexp.MustCompile(`<td class="cell-month-day[^"]*"`).FindAllString(row, -1)
		if len(cells) != 7 {
			t.Fatalf("a week row has %d day cells, want 7:\n%s", len(cells), row)
		}
		// Monday-start weeks, so the last two cells of every row are the
		// weekend and none of the first five is.
		for i, cell := range cells {
			weekend := strings.Contains(cell, "is-weekend")
			if want := i >= 5; weekend != want {
				t.Errorf("column %d of a Monday-start week has is-weekend=%v, want %v\n%s",
					i, weekend, want, cell)
			}
		}
	}

	// And the heading row, so the column reads as a column from the top.
	head := body[strings.Index(body, `class="month-grid"`):]
	head = head[:strings.Index(head, "</thead>")]
	if got := strings.Count(head, "is-weekend"); got != 2 {
		t.Errorf("%d weekday headings are marked as weekend, want 2 (Sat and Sun):\n%s",
			got, head)
	}
}

// TestTheWeekGridMarksItsWeekends.
//
// Every row of the table, not only the heading: the raster is meant to read as
// a column through the assignment rows and the day totals beneath them.
func TestTheWeekGridMarksItsWeekends(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCatalogue(t, srv)

	post(t, srv, "/entries", url.Values{
		"assignment_id": {"1"}, "date": {"2026-03-18"}, "start": {"09:00"},
		"duration": {"2h"}, "billable": {"on"},
	})

	body := get(t, srv, "/week?date=2026-03-16").Body.String()
	grid := body[strings.Index(body, `class="week-grid"`):]
	grid = grid[:strings.Index(grid, "</table>")]

	for _, section := range []struct {
		name, open, close string
	}{
		{"the heading row", "<thead>", "</thead>"},
		{"the assignment rows", "<tbody>", "</tbody>"},
		{"the day totals", "<tfoot>", "</tfoot>"},
	} {
		from := strings.Index(grid, section.open)
		to := strings.Index(grid, section.close)
		if from < 0 || to < 0 {
			t.Fatalf("no %s in the week grid", section.name)
		}
		part := grid[from:to]
		if got := strings.Count(part, "is-weekend"); got != 2 {
			t.Errorf("%s marks %d cells as weekend, want 2 (Saturday and "+
				"Sunday)\n%s", section.name, got, strings.TrimSpace(part))
		}
	}
}

// TestTheWeekendColourReachesTheDocument.
//
// The server writes a palette KEY onto <html> and the stylesheet turns it into
// a value, because the Content Security Policy forbids inline styles and a
// server-written style attribute would be dropped silently. So the thing to
// assert is that the key arrives, and that it is one the stylesheet knows.
func TestTheWeekendColourReachesTheDocument(t *testing.T) {
	srv, _ := newTestServer(t)

	// The default, before anybody touches the setting.
	if body := get(t, srv, "/month").Body.String(); !strings.Contains(body, `data-weekend="red"`) {
		t.Error(`a fresh instance does not carry data-weekend="red"`)
	}

	for _, colour := range []string{"teal", "purple", "slate"} {
		post(t, srv, "/settings", url.Values{
			"default_currency": {"SEK"}, "default_rounding": {"none"},
			"default_rate": {"0"}, "week_start": {"1"},
			"weekend_recording": {"on"}, "weekend_colour": {colour},
		})
		for _, screen := range []string{"/month", "/week", "/today"} {
			body := get(t, srv, screen).Body.String()
			if !strings.Contains(body, fmt.Sprintf(`data-weekend=%q`, colour)) {
				t.Errorf("%s does not carry the chosen weekend colour %q", screen, colour)
			}
		}
	}

	// A key the stylesheet has no rule for would render as no colour at all,
	// so the server is not allowed to pass one through.
	post(t, srv, "/settings", url.Values{
		"default_currency": {"SEK"}, "default_rounding": {"none"},
		"default_rate": {"0"}, "week_start": {"1"},
		"weekend_recording": {"on"}, "weekend_colour": {"chartreuse"},
	})
	body := get(t, srv, "/month").Body.String()
	if strings.Contains(body, "chartreuse") {
		t.Error("an unrecognised colour key reached the document")
	}
	if !strings.Contains(body, `data-weekend="slate"`) {
		t.Error("an unrecognised colour did not leave the previous one in force")
	}
}

// TestEveryPaletteKeyIsDrawable.
//
// The palette list is in the domain, the tokens are in the stylesheet, and the
// weekend's seven lines map one to the other. Three lists that have to agree,
// which is two opportunities for an eighth colour to be added in one place and
// render as nothing in another.
func TestEveryPaletteKeyIsDrawable(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	stylesheet := string(css)

	for _, key := range domain.ColourKeys {
		if !strings.Contains(stylesheet, "--entity-"+key+":") {
			t.Errorf("the palette offers %q but no theme defines --entity-%s", key, key)
		}
		if !strings.Contains(stylesheet, fmt.Sprintf(`[data-weekend=%q]`, key)) {
			t.Errorf("%q can be chosen as the weekend colour but the stylesheet "+
				"has no rule for it, so choosing it would draw no colour at all", key)
		}
	}

	// And nothing in the other direction: a rule for a key the settings form
	// will never offer is dead, and reads as though it were reachable.
	rules := regexp.MustCompile(`\[data-weekend="([a-z]+)"\]`).FindAllStringSubmatch(stylesheet, -1)
	if len(rules) == 0 {
		t.Fatal("no weekend colour rules in the stylesheet")
	}
	for _, rule := range rules {
		if !domain.IsColourKey(rule[1]) {
			t.Errorf("the stylesheet draws a weekend colour %q that names no "+
				"palette entry", rule[1])
		}
	}
}

// TestTheWeekendColoursStayReadableOnTheRaster.
//
// Two different backgrounds, because the cell has two.
//
// The hours and any ordinary text in a weekend cell sit on the raster, and are
// measured against its darkest point - a hatch stripe, since that is what a
// glyph can land on. The day number does not: it carries a backdrop of the
// cell's own surface precisely so that the seven colours it can take are never
// measured against a pattern. That was not the first design. The first design
// put the number on the raster, and this test found thirty-seven theme/colour
// pairs below AA with no percentage that was both visible and safe - two
// colours sit within 0.2 of the floor before any raster at all. Backing the
// number was the fix, so the two backgrounds here are the design, not an
// exemption from it.
func TestTheWeekendColoursStayReadableOnTheRaster(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	stylesheet := string(css)

	wash := weekendMixPercent(t, stylesheet, "--weekend-wash")
	hatch := weekendMixPercent(t, stylesheet, "--weekend-hatch")

	// The backdrop the stylesheet gives the number. Both grids are tables on
	// the raised surface, so there is one of these and not two - an earlier
	// version assumed the week grid's heading row painted the page colour and
	// was wrong, which a browser said and this file could not.
	backdrops := []string{"--surface-raised"}

	root := parseTokens(themeBlock(stylesheet, defaultTheme))
	for _, theme := range availableThemes {
		block := themeBlock(stylesheet, theme)
		if block == "" {
			t.Errorf("theme %q is offered but has no token block", theme)
			continue
		}
		tokens := parseTokens(block)

		text, okText := colourOf(tokens, root, "--text")
		if !okText {
			t.Errorf("theme %q does not define --text", theme)
			continue
		}

		// The day number, on its flat backdrop. Every palette colour, because
		// the setting offers every one of them and the theme is the reader's
		// own choice - there is no combination an administrator can avoid.
		for _, name := range backdrops {
			backdrop, ok := colourOf(tokens, root, name)
			if !ok {
				t.Errorf("theme %q does not define %s", theme, name)
				continue
			}
			for _, key := range domain.ColourKeys {
				ink, ok := colourOf(tokens, root, "--entity-"+key)
				if !ok {
					t.Errorf("theme %q does not define --entity-%s", theme, key)
					continue
				}
				if ratio := contrastRatio(ink, backdrop); ratio < 4.5 {
					t.Errorf("theme %s: a weekend date in %s is %.2f:1 on %s "+
						"(%s on %s), need 4.5:1",
						theme, key, ratio, name, ink, backdrop)
				}
			}
		}

		// Everything else in the cell, on the raster itself. Both layers are
		// mixed into transparent and painted over the surface, so the worst
		// point is the text colour composited twice.
		surface, ok := colourOf(tokens, root, "--surface-raised")
		if !ok {
			t.Errorf("theme %q does not define --surface-raised", theme)
			continue
		}
		striped := mixColours(text, mixColours(text, surface, wash), hatch)
		for _, name := range []string{"--text", "--text-muted"} {
			ink, ok := colourOf(tokens, root, name)
			if !ok {
				t.Errorf("theme %q does not define %s", theme, name)
				continue
			}
			if ratio := contrastRatio(ink, striped); ratio < 4.5 {
				t.Errorf("theme %s: %s on the weekend raster is %.2f:1 (%s on "+
					"%s), need 4.5:1", theme, name, ratio, ink, striped)
			}
		}
	}
}

// weekendMixPercent reads the percentage one of the raster's layers mixes at.
//
// Parsed out of the stylesheet rather than written here, so that nudging a
// layer for appearance re-runs the contrast arithmetic instead of leaving this
// test measuring a raster that no longer exists.
func weekendMixPercent(t *testing.T, css, token string) int {
	t.Helper()

	pattern := regexp.MustCompile(regexp.QuoteMeta(token) +
		`:\s*color-mix\(in srgb, var\(--text\) (\d+)%`)
	match := pattern.FindStringSubmatch(css)
	if match == nil {
		t.Fatalf("%s is not a color-mix of --text; the contrast arithmetic "+
			"below no longer describes what is drawn", token)
	}
	percent := 0
	if _, err := fmt.Sscanf(match[1], "%d", &percent); err != nil {
		t.Fatalf("unreadable percentage for %s: %v", token, err)
	}
	return percent
}

// TestTheWeekendColourIsNotOverriddenByTheGridsOwnRules.
//
// This is a regression test for a bug that was invisible in every other check.
// The weekend's colour rule ties on specificity with .cell-month-day .day-num,
// so it only wins by sitting later in the file. The first version of this
// feature put it earlier: the markup was correct, the setting saved correctly,
// the attribute reached the document, and every weekend date came out muted
// grey. Only looking at the rendered page showed it.
//
// Source order is the whole mechanism, so source order is what this asserts.
func TestTheWeekendColourIsNotOverriddenByTheGridsOwnRules(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	stylesheet := string(css)

	weekend := strings.Index(stylesheet, ".is-weekend .day-num")
	if weekend < 0 {
		t.Fatal("no rule colours the weekend's day numbers")
	}

	// Every rule that sets a colour on .day-num at the same specificity or
	// higher, and therefore has to come first.
	for _, rule := range []string{
		".cell-month-day .day-num",
		".cell-month-day.is-today .day-num",
	} {
		at := strings.Index(stylesheet, rule)
		if at < 0 {
			// The rule has been renamed or removed; the tie it created may be
			// gone, but this test can no longer prove anything about it.
			t.Errorf("%q is no longer in the stylesheet, so this test is not "+
				"checking what it claims to; re-derive the cascade", rule)
			continue
		}
		if at > weekend {
			t.Errorf("%q comes after the weekend colour rule and will override "+
				"it, leaving weekend dates in the ordinary colour while the "+
				"setting appears to work", rule)
		}
	}

	// .is-today is one class more specific, so beating it needs its own
	// selector rather than source order alone.
	if !strings.Contains(stylesheet, ".cell-month-day.is-today.is-weekend .day-num") {
		t.Error("nothing colours today's Saturday: .cell-month-day.is-today " +
			".day-num is more specific than the weekend rule, so today would " +
			"quietly override the setting on the one day somebody is looking at")
	}

	// The raster is a background-image, so anything that sets the background
	// shorthand on a cell that can be a weekend erases it.
	for _, rule := range []string{
		".cell-month-day.is-today", ".cell-month-day.outside", ".col-day.is-today",
	} {
		pattern := regexp.MustCompile(regexp.QuoteMeta(rule) + `\s*{[^}]*background:`)
		if pattern.MatchString(stylesheet) {
			t.Errorf("%s sets the background shorthand, which resets "+
				"background-image and erases the weekend raster on that cell; "+
				"use background-color", rule)
		}
	}
}
