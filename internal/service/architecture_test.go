package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Source-level rules about the service package.
//
// Both of these exist because the guarantee they protect is one that a reviewer
// has to notice the *absence* of. A read path that forgets to narrow, or a
// method that forgets to ask the authoriser, looks exactly like a correct one -
// there is no failing assertion, no error, no wrong number on a screen. Only a
// test that reads the code can see the omission.

// narrowers are the projection helpers. A method returning entries must call one
// of them, or be exempt for a stated reason.
var narrowers = []string{"narrowEntries", "narrowEntry", "narrowStream"}

// exemptFromNarrowing lists the entry-returning methods that do not project,
// with the reason each is safe.
//
// Every one of them is a *write*, and a client's authorisation is view-only:
// the authoriser refuses create, update, delete and proxy to the role outright,
// so a client cannot reach these to receive anything from them. Adding a name
// here is a claim that has to be true of the authoriser, not a way to quiet the
// test.
var exemptFromNarrowing = map[string]string{
	"StartTimer":   "write: a client may not create",
	"StopTimer":    "write: a client may not update",
	"CreateEntry":  "write: a client may not create",
	"UpdateEntry":  "write: a client may not update",
	"QuickAdd":     "write: a client may not create",
	"ApplyRoutine": "write: a client may not create",
	"AcceptEntry":  "write: a client may not decide a proposal",
	"Totals":       "pure arithmetic over entries the caller already has",
}

// TestEveryEntryReadIsNarrowed.
//
// ADR-0008 promises that a client receives a narrowed projection "before the
// data leaves the service layer, so a template bug cannot leak them". That is a
// promise about every path, and the failure mode is silent: a new read path
// returns full entries, every existing test passes, and the leak is only visible
// to somebody who thinks to look at a client's export.
func TestEveryEntryReadIsNarrowed(t *testing.T) {
	funcs := packageFuncs(t)
	for name, fn := range serviceMethods(t) {
		if reason, exempt := exemptFromNarrowing[name]; exempt {
			// A stated exemption still has to be about a method that exists;
			// a stale name here would quietly excuse a future method with the
			// same one.
			_ = reason
			continue
		}
		if !returnsEntries(fn) {
			continue
		}
		if !reaches(fn, narrowers, funcs, map[string]bool{}, 4) {
			t.Errorf("%s returns time entries without applying the client projection.\n"+
				"Call s.narrowEntries/narrowEntry/narrowStream, or add it to "+
				"exemptFromNarrowing with the reason a client cannot reach it.", name)
		}
	}
}

// TestNarrowingExemptionsAreLive.
//
// An exemption naming a method that no longer exists is worse than no exemption:
// it sits in the list looking considered, and silently excuses the next method
// somebody gives that name to.
func TestNarrowingExemptionsAreLive(t *testing.T) {
	methods := serviceMethods(t)
	for name := range exemptFromNarrowing {
		if _, ok := methods[name]; !ok {
			t.Errorf("exemptFromNarrowing names %s, which no longer exists", name)
		}
	}
}

// TestEveryServiceMethodAsksTheAuthoriser.
//
// ASR-005's stated proof, which TEST.md has claimed for some time and which did
// not exist: "a reflective test that enumerates service methods and fails on any
// that never calls Can()".
//
// A method reaches the authoriser directly, through a helper that does
// (entryAudience, canModify, effectiveScope and friends), or by taking the
// actor's identity in a way that is itself the check. The test looks for any of
// those, which makes it a check against *forgetting* rather than a proof of
// correctness - the RBAC matrix in internal/auth is what proves the decisions.
func TestEveryServiceMethodAsksTheAuthoriser(t *testing.T) {
	// Helpers that call Can on the caller's behalf, or that establish the actor
	// the decision is about.
	askers := []string{
		"Can", "entryAudience", "effectiveScope", "scopeFor", "canModify",
		"canDelete", "canDecide", "checkPeriodOpen", "MustUser", "requireAdmin",
		"narrowEntries", "narrowEntry",
	}
	// Methods that do not ask, each with the reason. Three shapes:
	//
	//   - arithmetic over values the caller already obtained, so the
	//     authorisation happened when they were fetched;
	//   - instance-wide facts with no subject to scope to;
	//   - operations with no acting user at all, run by the process itself at
	//     startup or on a timer.
	//
	// The last group is the one to watch. Each of these is reachable over HTTP
	// only through a handler that authorises first, which is a property of the
	// routing rather than of the method - exactly the arrangement that let a
	// client download a backup, and so exactly the arrangement worth naming
	// here rather than leaving implicit.
	open := map[string]string{
		"Now":       "reads the injected clock",
		"WithBlobs": "wiring, called at construction",

		"Totals":        "arithmetic over entries the caller already holds",
		"ExpenseTotals": "arithmetic over expenses the caller already holds",
		"NeedsReceipt":  "a rule applied to an expense the caller already holds",
		"ParseQuickAdd": "parses text; records nothing",

		"Settings":             "instance-wide display settings, read by the page shell for every user",
		"IdleThresholdSeconds": "one instance-wide number, read by the page shell",
		"TermsOn":              "resolves contract terms for a named customer; callers reach it with ids they were already authorised for",
		"TermsCurrency":        "as TermsOn",

		"LocalUser":       "local mode's single identity, resolved before there is an actor",
		"EnsureLocalUser": "creates that identity at startup",

		"ListBackupFiles": "lists files on disk; the handler calls AuthorizeBackup first",
		"PruneBackups":    "housekeeping, run by the process on a timer",
		"WriteBackupFile": "housekeeping, run by the process on a timer",
		"SweepBlobs":      "housekeeping, run by the process on a timer",

		"ImportCalendar":    "takes a preview the caller was authorised to produce",
		"ApplyAllRoutines":  "applies the caller's own routines, each of which authorises",
		"PreviewAttachment": "renders bytes the caller already opened",
		"TextPreviews":      "renders bytes the caller already opened",
	}

	methods := serviceMethods(t)
	// A reason naming a method that no longer exists silently excuses the next
	// method somebody gives that name to.
	for name := range open {
		if _, ok := methods[name]; !ok {
			t.Errorf("the open map names %s, which no longer exists", name)
		}
	}

	funcs := packageFuncs(t)
	for name, fn := range methods {
		if _, ok := open[name]; ok {
			continue
		}
		if !reaches(fn, askers, funcs, map[string]bool{}, 4) {
			t.Errorf("%s never reaches the authoriser: no Can, no scope, no actor.\n"+
				"If it is genuinely open, say so in the `open` map above.", name)
		}
	}
}

// packageFuncs returns every function and method in the package, by simple name.
//
// Both scans follow calls one function at a time, and almost every service
// method delegates: ApproveWeek calls decideWeek, Attachments calls
// authorizeOwner, Entries calls searchEntries. A scan that looked only at the
// method's own body would report all three as unchecked, which is how a test
// like this turns into a list of exceptions that nobody reads.
func packageFuncs(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	return funcs
}

// reaches reports whether fn calls any of the named functions, directly or
// through package-local helpers.
//
// Depth-limited and cycle-safe. Four levels is more than the deepest real chain
// here and stops the walk from wandering into unrelated code and finding an
// authorisation call that has nothing to do with the caller.
func reaches(fn *ast.FuncDecl, names []string, funcs map[string]*ast.FuncDecl, seen map[string]bool, depth int) bool {
	if fn == nil || fn.Body == nil || depth < 0 {
		return false
	}
	if callsAnyOf(fn, names) {
		return true
	}

	var found bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var callee string
		switch target := call.Fun.(type) {
		case *ast.Ident:
			callee = target.Name
		case *ast.SelectorExpr:
			callee = target.Sel.Name
		}
		if callee == "" || seen[callee] {
			return true
		}
		next, ok := funcs[callee]
		if !ok {
			return true
		}
		seen[callee] = true
		if reaches(next, names, funcs, seen, depth-1) {
			found = true
		}
		return true
	})
	return found
}

// serviceMethods returns the exported methods on *Service, by name.
func serviceMethods(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found; the test is looking in the wrong place")
	}

	methods := map[string]*ast.FuncDecl{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name == nil || !fn.Name.IsExported() {
				continue
			}
			if !isServiceReceiver(fn.Recv) {
				continue
			}
			methods[fn.Name.Name] = fn
		}
	}
	if len(methods) < 20 {
		t.Fatalf("only found %d service methods; the scan is not finding the package",
			len(methods))
	}
	return methods
}

// isServiceReceiver reports whether the receiver is Service or *Service.
func isServiceReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) != 1 {
		return false
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "Service"
}

// returnsEntries reports whether any result mentions domain.TimeEntry.
func returnsEntries(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	var found bool
	for _, result := range fn.Type.Results.List {
		ast.Inspect(result.Type, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "domain" && selector.Sel.Name == "TimeEntry" {
				found = true
			}
			return true
		})
	}
	return found
}

// callsAnyOf reports whether the body calls any of the named functions, by
// simple name - so both s.narrowEntries(...) and narrowFilter(...) are seen.
func callsAnyOf(fn *ast.FuncDecl, names []string) bool {
	if fn.Body == nil {
		return false
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}

	var found bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch target := call.Fun.(type) {
		case *ast.Ident:
			if wanted[target.Name] {
				found = true
			}
		case *ast.SelectorExpr:
			if wanted[target.Sel.Name] {
				found = true
			}
		}
		return true
	})
	return found
}
