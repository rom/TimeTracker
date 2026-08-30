package repocheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Shared machinery for the scans in this package.
//
// All of them need the same three things: where the repository is, which files
// are its Go source, and a parsed syntax tree per file. Doing that once means a
// scan is the rule it enforces and nothing else.

// repoRoot returns the repository root, which is two levels up from this
// package.
//
// Resolved rather than assumed: a wrong path here would make every scan in the
// package walk an empty tree and pass, which is the one failure a source scan
// must not have. Callers get a fatal error instead.
func repoRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s does not look like the repository root: %v", root, err)
	}
	return root
}

// sourceFile is one parsed non-test Go file.
type sourceFile struct {
	// path is relative to the repository root, so failures name something a
	// reader can open.
	path string
	fset *token.FileSet
	file *ast.File
}

// pos renders a node's position as path:line, so a failure names something a
// reader can jump to.
func (s sourceFile) pos(node ast.Node) string {
	return s.path + ":" + strconv.Itoa(s.fset.Position(node.Pos()).Line)
}

// goSources parses every non-test Go file under the given repository-relative
// directories.
//
// Test files are excluded throughout. The rules here constrain what ships: a
// test may declare a float, seed an audit row directly, or import whatever it
// needs to prove something.
func goSources(t *testing.T, dirs ...string) []sourceFile {
	t.Helper()

	root := repoRoot(t)
	var sources []sourceFile
	for _, dir := range dirs {
		walked := 0
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				// Nothing generated or vendored is ours to hold to these rules.
				if name := entry.Name(); name == "testdata" || name == "vendor" || name == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			walked++
			relative, err := filepath.Rel(root, path)
			if err != nil {
				relative = path
			}
			fset := token.NewFileSet()
			parsed, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", relative, err)
			}
			sources = append(sources, sourceFile{path: filepath.ToSlash(relative), fset: fset, file: parsed})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
		if walked == 0 {
			t.Fatalf("no Go files under %s; the scan is looking in the wrong place", dir)
		}
	}
	return sources
}

// readRepoFile reads one file from the repository root.
func readRepoFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

// filesMatching returns every file under dir whose name ends in suffix.
func filesMatching(t *testing.T, dir, suffix string) []string {
	t.Helper()

	root := repoRoot(t)
	var found []string
	err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, suffix) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(found) == 0 {
		t.Fatalf("no %s files under %s; the scan is looking in the wrong place", suffix, dir)
	}
	return found
}
