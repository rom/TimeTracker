package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The application on a screen narrower than a desk.
//
// DESIGN.md §11 has always said the interface is responsive. It was not: the
// top bar was a flex row with no wrap holding thirteen navigation links, two
// pickers, a clock, a date and a name, so below 1440px it simply ran off the
// side and took the page's horizontal scrollbar with it - 374px of overflow on
// a 390px phone, and still 46px at 1280, where it clipped the signed-in name.
// Every screen, every size below a large laptop.
//
// None of this can be measured from Go. What can be checked is the structure
// that makes it true, and that is what these tests do: the rules that wrap, and
// the wrapper that every wide table sits in. The browser measurements are in
// docs/TEST.md §5 and were taken by hand; these keep the structure from
// quietly reverting between those passes.

// stylesheet reads the embedded CSS once.
func stylesheet(t *testing.T) string {
	t.Helper()

	css, err := staticFS.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	return string(css)
}

// TestTheRowsThatMustWrapDoWrap.
//
// Each of these is a row of controls whose natural width exceeds a phone, and
// each one in turn was the thing still pushing the page sideways once the row
// above it had been fixed. They are listed together because the lesson is the
// list: a flex row of unknown-width controls in a layout that claims to be
// responsive needs flex-wrap, and the default is the wrong one.
func TestTheRowsThatMustWrapDoWrap(t *testing.T) {
	css := stylesheet(t)

	for _, row := range []struct {
		selector, why string
	}{
		{".topbar", "the whole bar, so the right-hand cluster drops below the navigation rather than off the edge"},
		{".mainnav", "thirteen links will not fit on one line on a phone"},
		{".topbar-right", "two pickers, a clock, a date, a name and sign-out"},
		{".date-nav", "previous, today, next, a date picker, Go and Month"},
		{".export-bar", "six export formats and a preview link"},
	} {
		rule := ruleFor(css, row.selector)
		if rule == "" {
			t.Errorf("%s is no longer in the stylesheet, so this test is not "+
				"checking what it claims to", row.selector)
			continue
		}
		if !strings.Contains(rule, "flex-wrap: wrap") {
			t.Errorf("%s does not wrap, and it must: %s\n%s",
				row.selector, row.why, strings.TrimSpace(rule))
		}
	}

	// The exception, and it is a real one: the left-hand rail is a column, and
	// a column that wraps wraps into a second column - which in a rail with a
	// max-height of one viewport silently splits the navigation in two.
	rail := ruleFor(css, `[data-nav="left"] .topbar`)
	if !strings.Contains(rail, "flex-wrap: nowrap") {
		t.Error(`[data-nav="left"] .topbar must say flex-wrap: nowrap; it is a ` +
			`column, and the top bar's wrapping would split the rail into two columns`)
	}
}

// TestAWideTableScrollsItselfRatherThanThePage.
//
// Two halves, and both are needed.
//
// The first is that paint containment is declared. overflow-x alone clips the
// table visually but Chromium still propagates the table's layout overflow to
// the root, so the whole page scrolls sideways behind an apparently contained
// table - measured at 74px of page scroll on a 390px screen with the table
// correctly clipped at 350px. Paint containment states what is already true of
// a scroll container and stops the propagation.
//
// The second is that every table actually sits in one. That is the half that
// rots: the rule is invisible from the template, so a new screen with a wide
// table looks finished and reintroduces the bug on one page.
func TestAWideTableScrollsItselfRatherThanThePage(t *testing.T) {
	css := stylesheet(t)

	scroll := ruleFor(css, ".grid-scroll")
	if !strings.Contains(scroll, "overflow-x: auto") {
		t.Error(".grid-scroll no longer scrolls its own overflow")
	}
	if !strings.Contains(scroll, "contain:") {
		t.Error(".grid-scroll must declare paint containment; without it the " +
			"table is clipped but the page scrolls sideways behind it anyway")
	}

	opening := regexp.MustCompile(`<table(?:[ \t\n][^>]*)?>`)
	templates, err := filepath.Glob("templates/*.html")
	if err != nil || len(templates) == 0 {
		t.Fatalf("no templates found: %v", err)
	}

	for _, path := range templates {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		markup := string(source)
		for _, at := range opening.FindAllStringIndex(markup, -1) {
			before := strings.TrimRight(markup[:at[0]], " \t\r\n")
			if strings.HasSuffix(before, `<div class="grid-scroll">`) {
				continue
			}
			// Enough of the line to find it by eye, without printing a table.
			line := 1 + strings.Count(markup[:at[0]], "\n")
			t.Errorf(`%s:%d: %s is not wrapped in <div class="grid-scroll">, `+
				`so on a narrow screen it widens the whole page instead of `+
				`scrolling itself`,
				filepath.Base(path), line, strings.TrimSpace(markup[at[0]:at[1]]))
		}
	}
}

// ruleFor returns the declaration block of the first rule whose selector list
// begins with the given selector.
//
// Deliberately simple: this stylesheet is hand-written, one rule per selector,
// and a parser that handled every CSS edge case would be a worse test than one
// that fails loudly when the file stops looking the way it looks.
func ruleFor(css, selector string) string {
	for _, prefix := range []string{"\n" + selector + " {", "\n" + selector + ",", "\n" + selector + "{"} {
		at := strings.Index(css, prefix)
		if at < 0 {
			continue
		}
		open := strings.Index(css[at:], "{")
		close := strings.Index(css[at:], "}")
		if open < 0 || close < 0 || close < open {
			return ""
		}
		return css[at+open : at+close]
	}
	return ""
}
