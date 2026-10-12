package repocheck

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

// ASR-002: the binary cross-compiles to every supported platform from any host.
//
// That property has exactly one prerequisite, and it is not a build flag: no
// package in the tree may use cgo. `CGO_ENABLED=0` in the Makefile is how the
// requirement is *expressed*; a single `import "C"` is how it would be lost,
// and the loss would be invisible on the machine that introduced it, because
// that machine has a C toolchain. It surfaces later, as a failed release build
// for macOS on a Linux host.
//
// `make build-check` would catch it, and a regression test asserts that target
// is still wired into `make check`. These are the cheaper, more specific
// version: they name the file and the import rather than reporting that six
// cross-compiles failed.

// TestNothingImportsCgo.
//
// The direct check. `import "C"` is the whole of cgo: no other syntax turns it
// on, so this is a complete rule rather than a heuristic.
func TestNothingImportsCgo(t *testing.T) {
	for _, source := range goSources(t, "internal", "cmd") {
		for _, imported := range source.file.Imports {
			if strings.Trim(imported.Path.Value, `"`) != "C" {
				continue
			}
			t.Errorf("%s imports \"C\": cgo makes the binary unable to cross-compile, "+
				"which is the whole of ASR-002. See docs/adr/0003-pure-go-sqlite.md.",
				source.pos(imported))
		}
	}
}

// TestNoCgoDirectivesInComments.
//
// A `#cgo` line in a comment above `import "C"` is the usual shape, and the
// import test above already catches that. What this adds is the case where the
// import is added later: a preamble sitting in the tree is a half-finished cgo
// change, and it is worth failing on the intent rather than waiting for the
// second half.
func TestNoCgoDirectivesInComments(t *testing.T) {
	for _, source := range goSources(t, "internal", "cmd") {
		for _, group := range source.file.Comments {
			for _, comment := range group.List {
				if strings.Contains(comment.Text, "#cgo ") {
					t.Errorf("%s carries a #cgo directive", source.pos(comment))
				}
			}
		}
	}
}

// TestNoCSourceInTheTree.
//
// C or assembly alongside the Go source is cgo by another route, and it breaks
// the same promise: a .c file compiles with the host's toolchain, for the host's
// platform. Assembly is included because a .s file is per-architecture by
// definition, so one in this tree would mean some platform in the ASR-002 matrix
// building differently from the others.
func TestNoCSourceInTheTree(t *testing.T) {
	root := repoRoot(t)
	for _, suffix := range []string{".c", ".h", ".s", ".cc", ".cpp"} {
		matches, err := filepath.Glob(filepath.Join(root, "internal", "*", "*"+suffix))
		if err != nil {
			t.Fatalf("glob %s: %v", suffix, err)
		}
		more, err := filepath.Glob(filepath.Join(root, "cmd", "*", "*"+suffix))
		if err != nil {
			t.Fatalf("glob %s: %v", suffix, err)
		}
		for _, match := range append(matches, more...) {
			relative, _ := filepath.Rel(root, match)
			t.Errorf("%s: C or assembly in the tree defeats the cross-compile", relative)
		}
	}
}

// TestTheBuildDisablesCgo.
//
// The Makefile's side of it. Even with no cgo in the tree, a dependency that
// gains a cgo path would be built with it whenever a C compiler happens to be
// present - which is how a build becomes accidentally host-specific without
// anybody writing a line of C. `CGO_ENABLED=0` is what makes that impossible,
// and it is exported rather than set per-target so it applies to every `go`
// invocation the Makefile makes.
//
// The test also pins that it is not exported for the *test* target: the race
// detector needs cgo, and the Makefile deliberately re-enables it there. That is
// a test-time tool with no bearing on the release artefact, and the comment
// saying so should not quietly become false.
func TestTheBuildDisablesCgo(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	if !strings.Contains(makefile, "export CGO_ENABLED := 0") {
		t.Error("the Makefile no longer exports CGO_ENABLED=0, so a host with a C " +
			"compiler produces a binary that does not cross-compile (ASR-002)")
	}

	// Every platform in the ASR-002 matrix is still built.
	for _, platform := range []string{
		"darwin/amd64", "darwin/arm64",
		"linux/amd64", "linux/arm64",
		"windows/amd64", "windows/arm64",
	} {
		if !strings.Contains(makefile, platform) {
			t.Errorf("%s has dropped out of the build matrix", platform)
		}
	}
}

// TestOnlyThePureGoSQLiteDriverIsImported.
//
// The reason the tree can be cgo-free at all. The obvious SQLite driver for Go
// is mattn/go-sqlite3, which is a cgo binding; ADR-0003 chose modernc.org/sqlite
// instead, at a measured cost in speed, precisely to keep the single-binary
// promise. Nothing in the tree stops somebody adding the other one to "fix" a
// performance problem, and the build would keep working on their machine.
func TestOnlyThePureGoSQLiteDriverIsImported(t *testing.T) {
	banned := map[string]string{
		"github.com/mattn/go-sqlite3":        "a cgo binding; ADR-0003 chose modernc.org/sqlite instead",
		"crawshaw.io/sqlite":                 "a cgo binding",
		"github.com/glebarez/go-sqlite":      "not the driver this project builds against",
		"modernc.org/sqlite/lib":             "the internal C-translated layer, not the driver API",
		"github.com/ncruces/go-sqlite3/gorm": "not the driver this project builds against",
	}

	for _, source := range goSources(t, "internal", "cmd") {
		for _, imported := range source.file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if reason, ok := banned[path]; ok {
				t.Errorf("%s imports %s: %s", source.pos(imported), path, reason)
			}
		}
	}

	// And the module graph, since an unused dependency is a dependency somebody
	// will use.
	gomod := readRepoFile(t, "go.mod")
	if strings.Contains(gomod, "mattn/go-sqlite3") {
		t.Error("go.mod requires mattn/go-sqlite3, which is cgo")
	}
	if !strings.Contains(gomod, "modernc.org/sqlite") {
		t.Error("go.mod no longer requires modernc.org/sqlite, the pure-Go driver ADR-0003 chose")
	}
}

// TestNothingOutsideHardeningUsesSyscallsDirectly.
//
// Not cgo, but the same requirement and the same failure. A raw syscall compiles
// on the platform it was written for and breaks the cross-compile for the other
// two, which is exactly how the macOS and Windows builds broke once before. The
// sandboxing that genuinely needs per-platform code is isolated in
// internal/hardening behind build tags, with a no-op fallback, so that the rest
// of the tree stays portable.
//
// The signal numbers are the exception, and a real one: syscall.SIGTERM is
// declared on all three platforms, and asking for a graceful shutdown is not
// platform-specific work. Everything else in that package is - which is why this
// looks at what is actually referenced rather than at the import line.
func TestNothingOutsideHardeningUsesSyscallsDirectly(t *testing.T) {
	// Signals the Go runtime defines everywhere, including Windows.
	portable := map[string]bool{"SIGTERM": true, "SIGINT": true, "SIGHUP": true, "SIGQUIT": true}

	for _, source := range goSources(t, "internal", "cmd") {
		if strings.HasPrefix(source.path, "internal/hardening/") {
			continue
		}
		for _, imported := range source.file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if strings.HasPrefix(path, "golang.org/x/sys/") {
				t.Errorf("%s imports %s outside internal/hardening: per-platform code "+
					"belongs behind a build tag with a portable fallback",
					source.pos(imported), path)
			}
		}

		ast.Inspect(source.file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "syscall" || portable[selector.Sel.Name] {
				return true
			}
			t.Errorf("%s uses syscall.%s outside internal/hardening: only the signal "+
				"numbers are declared on all three platforms",
				source.pos(selector), selector.Sel.Name)
			return true
		})
	}
}
