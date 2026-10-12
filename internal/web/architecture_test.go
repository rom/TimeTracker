package web

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLayeringIsEnforced checks the rule that makes two requirements provable by
// reading one package instead of auditing every handler:
//
//	internal/web must not import internal/store.
//
// If a handler could reach the store directly, it could read or write a record
// without an authorisation check (ASR-005) and without an audit row (ASR-006),
// and the guarantee would hold only by everyone remembering.
// See docs/adr/0012-layered-package-structure.md.
func TestLayeringIsEnforced(t *testing.T) {
	const forbidden = "github.com/rom/timetracker/internal/store"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found; the test is looking in the wrong place")
	}

	fset := token.NewFileSet()
	for _, file := range files {
		// Test files are exempt: a test wires a real store to exercise the
		// handlers, which is the point. The rule constrains what ships.
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		parsed, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if path == forbidden {
				t.Errorf("%s imports %s: handlers must go through internal/service, "+
					"so that no request can reach the database without an "+
					"authorisation check and an audit record", file, forbidden)
			}
		}
	}
}

// TestNoFloatInPersistedTypes guards the rule from
// docs/adr/0014-exact-money-and-duration.md at the layer where user input is
// decoded: a float parsed here would flow straight into a stored amount.
func TestNoFloatInPersistedTypes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if strings.Contains(string(src), "ParseFloat") {
			t.Errorf("%s parses a float: money and durations are integers "+
				"(minor units and whole seconds) throughout", file)
		}
	}
}

// TestNoTemplateUsesAnInlineStyle.
//
// The Content Security Policy has no 'unsafe-inline' in style-src, which means
// a browser drops a style attribute instead of applying it. Nothing breaks
// loudly: the page renders, the element is there, and it is simply the wrong
// size or colour. The budget report shipped with every bar at full width for
// exactly this reason, and the bug was found by looking at a screenshot of a
// different screen - which is not a method.
//
// So: no style attributes in templates at all. A value that varies gets a class
// the stylesheet defines, which is also the only way the eight themes can
// redefine it.
func TestNoTemplateUsesAnInlineStyle(t *testing.T) {
	files, err := filepath.Glob("templates/*.html")
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no templates found; the scan is looking in the wrong place")
	}

	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for number, line := range strings.Split(string(source), "\n") {
			if !strings.Contains(line, "style=") {
				continue
			}
			t.Errorf("%s:%d has an inline style, which the Content Security Policy "+
				"drops:\n    %s\nUse a class the stylesheet defines - see barStep "+
				"for the pattern when the value varies.",
				file, number+1, strings.TrimSpace(line))
		}
	}
}

// TestEveryBarWidthHasAClass.
//
// The other half of the same guard. barStep rounds to a step, and a step with
// no rule in the stylesheet is a bar of no width at all - which looks like
// "nothing recorded" rather than like a missing class.
func TestEveryBarWidthHasAClass(t *testing.T) {
	stylesheet, err := os.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatalf("read the stylesheet: %v", err)
	}
	css := string(stylesheet)

	steps := templateFuncs()["barStep"].(func(int) int)
	for percent := -20; percent <= 140; percent++ {
		class := ".bar-step-" + strconv.Itoa(steps(percent))
		if !strings.Contains(css, class+" ") && !strings.Contains(css, class+"{") {
			t.Fatalf("%d%% rounds to %s, which the stylesheet does not define",
				percent, class)
		}
	}
}
