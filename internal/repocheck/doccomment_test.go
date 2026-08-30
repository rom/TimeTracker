package repocheck

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ASR-011: the code is readable by somebody who did not write it.
//
// `gofmt` and `go vet` are enforced by `make check`, and they are what most
// projects mean by this. Neither says anything about whether the code can be
// understood. A file can be perfectly formatted, pass every vet check, and
// consist of forty exported functions whose names are the only explanation of
// what they do.
//
// This is the part of the requirement a machine can still check: every exported
// symbol says what it is for, and every package says what it is. It is a floor
// rather than a standard - a doc comment reading "// Foo does foo." satisfies it
// and helps nobody - but it is a floor that catches the specific thing that
// happens under time pressure, which is a symbol added to an exported API with
// no explanation at all and no reviewer noticing among a hundred other lines.

// TestEveryPackageSaysWhatItIsFor.
//
// The package comment is the one piece of documentation that is read *before*
// somebody decides which file to open, so it is the one that decides whether
// they open the right one. It is also the easiest to lose: a package that gains
// its first file from a split has no comment anywhere, and nothing complains.
func TestEveryPackageSaysWhatItIsFor(t *testing.T) {
	documented := map[string]bool{}
	for _, source := range goSources(t, "internal", "cmd") {
		dir := filepath.Dir(source.path)
		if source.file.Doc != nil && strings.TrimSpace(source.file.Doc.Text()) != "" {
			documented[dir] = true
			continue
		}
		if _, seen := documented[dir]; !seen {
			documented[dir] = false
		}
	}

	var missing []string
	for dir, hasComment := range documented {
		if !hasComment {
			missing = append(missing, dir)
		}
	}
	sort.Strings(missing)
	for _, dir := range missing {
		t.Errorf("package %s has no package comment: nothing in the tree says what it "+
			"is for or when to reach for it (ASR-011)", dir)
	}
}

// TestEveryExportedSymbolIsDocumented.
//
// Exported only. An unexported helper is read by somebody who is already inside
// the file and can see its single call site; an exported one is read by somebody
// who cannot.
//
// A member of a const or var block counts as documented if the block says what
// the set is for, or if the line carries a trailing comment. That is how the
// roles and the export formats are written, and it is the right shape for them:
// twelve one-line comments above twelve constants would say less than one
// sentence above the block plus a few words per line.
func TestEveryExportedSymbolIsDocumented(t *testing.T) {
	var undocumented []string

	for _, source := range goSources(t, "internal", "cmd") {
		for _, decl := range source.file.Decls {
			switch declaration := decl.(type) {
			case *ast.FuncDecl:
				if !declaration.Name.IsExported() || hasText(declaration.Doc) {
					continue
				}
				// A method on an unexported type is not part of any API this
				// package offers: the only way to hold one is to be inside the
				// package already, with the type declaration a few lines away.
				// Error() on a private error type is the common case.
				if fn := declaration; fn.Recv != nil && !exportedReceiver(fn) {
					continue
				}
				undocumented = append(undocumented,
					source.pos(declaration)+": func "+receiverName(declaration)+declaration.Name.Name)

			case *ast.GenDecl:
				if declaration.Tok == token.IMPORT {
					continue
				}
				for _, spec := range declaration.Specs {
					switch specification := spec.(type) {
					case *ast.TypeSpec:
						if !specification.Name.IsExported() {
							continue
						}
						if hasText(specification.Doc) || hasText(declaration.Doc) {
							continue
						}
						undocumented = append(undocumented,
							source.pos(specification)+": type "+specification.Name.Name)

					case *ast.ValueSpec:
						if hasText(specification.Doc) || hasText(declaration.Doc) ||
							hasText(specification.Comment) {
							continue
						}
						for _, name := range specification.Names {
							if !name.IsExported() {
								continue
							}
							undocumented = append(undocumented,
								source.pos(specification)+": "+declaration.Tok.String()+" "+name.Name)
						}
					}
				}
			}
		}
	}

	sort.Strings(undocumented)
	for _, symbol := range undocumented {
		t.Errorf("%s is exported with no doc comment (ASR-011)", symbol)
	}
}

// exportedReceiver reports whether a method's receiver type is exported.
func exportedReceiver(fn *ast.FuncDecl) bool {
	name := strings.TrimSuffix(receiverName(fn), ".")
	return name != "" && ast.IsExported(name)
}

// hasText reports whether a comment group exists and says something.
func hasText(group *ast.CommentGroup) bool {
	return group != nil && strings.TrimSpace(group.Text()) != ""
}

// receiverName renders a method's receiver type as "Type.", so a failure names
// Customer.ForClient rather than three unrelated ForClients.
func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if index, ok := expr.(*ast.IndexExpr); ok { // a generic receiver
		expr = index.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name + "."
	}
	return ""
}
