package repocheck

import (
	"go/ast"
	"regexp"
	"strings"
	"testing"
)

// ASR-008: time recorded on somebody's behalf counts for nothing until they
// accept it.
//
// The workflow half of that is well covered by running code: a proposal is
// pending, an accepted one is confirmed, a rejected one is kept rather than
// deleted. What was not covered is the half that makes it *true* rather than
// merely intended - that no total anywhere includes a proposal nobody has
// accepted.
//
// Those totals are asserted one at a time by tests over the report, the week
// banner and the budget report. A new one - a dashboard, a per-customer summary,
// an invoice draft - would be written by copying an existing query, and copying
// the wrong one is not a mistake any test would notice. Every existing assertion
// would still pass, and the new number would be quietly wrong in the direction
// nobody checks: too high, by exactly the work somebody has not agreed they did.
//
// So this enumerates instead. Every aggregation over time entries, in SQL and in
// Go, must exclude what does not count - or say why it does not have to.

// countingSQL is the rule spelled as SQL: confirmed, and not flagged for review.
//
// The two conditions are written separately rather than as a status list so that
// the partial indexes match them (ADR-0032), which is also why this looks for
// both spellings rather than for a helper call.
var countingSQL = []*regexp.Regexp{
	regexp.MustCompile(`status\s*=\s*'confirmed'`),
	regexp.MustCompile(`flagged\s*=\s*0`),
}

// aggregates matches a SQL total.
var aggregates = regexp.MustCompile(`(?i)\b(SUM|COUNT|AVG|TOTAL)\s*\(`)

// overEntries matches a query that reads the time entries table.
var overEntries = regexp.MustCompile(`(?i)\bFROM\s+time_entries\b`)

// exemptAggregations lists the queries that aggregate over time entries without
// the counting predicate, each with the reason it is not a total.
//
// Every one of these is a *count of rows for a purpose other than measuring
// work*. Adding a name here is a claim that the number is not consumption,
// hours, or money - not a way to quiet the test.
var exemptAggregations = map[string]string{
	"CountEntries": "the listing's row count, built from the caller's own filter " +
		"through buildConditions - it must match the rows on screen, including " +
		"the pending ones the screen shows",
	"CountPending": "counts what is awaiting a decision, so pending is the point",
	"QuickStartAssignments": "ranks assignments by how often they were used; a " +
		"proposal is still evidence somebody works on that assignment",
}

// TestEveryEntryAggregationExcludesUnacceptedTime.
//
// The SQL half. A function's query is taken as everything it builds from string
// literals, because the conditions are assembled beside the SELECT and the two
// only mean anything together.
func TestEveryEntryAggregationExcludesUnacceptedTime(t *testing.T) {
	found := 0
	checked := 0

	for _, source := range goSources(t, "internal/store") {
		for _, decl := range source.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			query := stripSQLComments(literalText(fn))
			if !overEntries.MatchString(query) || !aggregates.MatchString(query) {
				continue
			}
			found++

			if reason, exempt := exemptAggregations[fn.Name.Name]; exempt {
				_ = reason
				continue
			}
			checked++

			var missing []string
			for _, condition := range countingSQL {
				if !condition.MatchString(query) {
					missing = append(missing, condition.String())
				}
			}
			// A query built from an EntryFilter inherits the rule from the
			// caller's CountingOnly, which the service sets. That is a different
			// mechanism, not a missing one.
			if len(missing) > 0 && !callsAny(fn, "buildConditions", "buildSearch") {
				t.Errorf("%s aggregates over time entries without %s.\n"+
					"Unaccepted proxy time and entries flagged for review are not "+
					"work anybody has agreed happened (ASR-008). Add the conditions, "+
					"build the query from an EntryFilter with CountingOnly, or name "+
					"the function in exemptAggregations with the reason it is not a total.",
					source.pos(fn), strings.Join(missing, " and "))
			}
		}
	}

	// The scan has to be finding queries at all. A refactor that moved the SQL
	// into a builder would leave this passing over nothing.
	if found < 4 {
		t.Errorf("only found %d aggregations over time entries; the scan is no longer "+
			"reading the queries", found)
	}
	if checked == 0 {
		t.Error("every aggregation found is exempt, so this test is asserting nothing")
	}
}

// TestExemptAggregationsAreLive.
//
// A reason naming a function that no longer exists sits in the list looking
// considered, and silently excuses the next function given that name.
func TestExemptAggregationsAreLive(t *testing.T) {
	names := map[string]bool{}
	for _, source := range goSources(t, "internal/store") {
		for _, decl := range source.file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
				names[fn.Name.Name] = true
			}
		}
	}
	for name := range exemptAggregations {
		if !names[name] {
			t.Errorf("exemptAggregations names %s, which no longer exists in internal/store", name)
		}
	}

	accumulating := map[string]bool{}
	for _, source := range goSources(t, "internal/service", "internal/export") {
		for _, decl := range source.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name != nil && fn.Body != nil && accumulatesAQuantity(fn) {
				accumulating[fn.Name.Name] = true
			}
		}
	}
	for name := range exemptGoTotals {
		if !accumulating[name] {
			t.Errorf("exemptGoTotals names %s, which no longer adds up a total", name)
		}
	}
}

// exemptGoTotals lists the Go accumulations that do not consult Counts, with the
// reason each is not summing stored entries.
var exemptGoTotals = map[string]string{
	"ParseTimeCSV": "totals rows parsed from a file that has not been imported " +
		"yet; they are not entries and have no status",
	"ParseCalendar":  "as ParseTimeCSV, for a calendar being previewed",
	"ImportCalendar": "totals what this import just created, all of it confirmed",
	"buildTimeline": "measures what falls outside the visible window of the day " +
		"screen, which deliberately shows provisional entries - an arrow that " +
		"ignored them would point at empty space",
}

// accumulatedFields are the quantities a total is made of.
var accumulatedFields = map[string]bool{
	"Seconds": true, "TotalSeconds": true, "DurationSeconds": true,
	"BillableSeconds": true, "SummedSeconds": true,
	"AmountMinor": true, "Minor": true, "UsedMinor": true, "UsedSeconds": true,
}

// TestEveryGoTotalExcludesUnacceptedTime.
//
// The other half, and the one that matters most for reports: the export writers
// and the approval report sum in Go, over entries the store has already handed
// back. A `+=` without a Counts guard is how a proposal reaches an invoice.
func TestEveryGoTotalExcludesUnacceptedTime(t *testing.T) {
	found := 0

	for _, source := range goSources(t, "internal/service", "internal/export") {
		for _, decl := range source.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil || !accumulatesAQuantity(fn) {
				continue
			}
			found++

			if _, exempt := exemptGoTotals[fn.Name.Name]; exempt {
				continue
			}
			if callsAny(fn, "Counts") {
				continue
			}
			t.Errorf("%s (%s) adds up durations or amounts without asking Counts().\n"+
				"A pending proposal and an entry flagged for review must not reach a "+
				"total (ASR-008). Guard the loop, or name the function in "+
				"exemptGoTotals with the reason it is not summing stored entries.",
				fn.Name.Name, source.pos(fn))
		}
	}

	if found < 5 {
		t.Errorf("only found %d functions accumulating a total; the scan is no longer "+
			"reading the code", found)
	}
}

// accumulatesAQuantity reports whether the body does `x.Something += y` where
// Something is a duration or an amount.
func accumulatesAQuantity(fn *ast.FuncDecl) bool {
	var found bool
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || assign.Tok.String() != "+=" {
			return true
		}
		for _, target := range assign.Lhs {
			ast.Inspect(target, func(inner ast.Node) bool {
				if selector, ok := inner.(*ast.SelectorExpr); ok && accumulatedFields[selector.Sel.Name] {
					found = true
				}
				return true
			})
		}
		return true
	})
	return found
}

// literalText joins every string literal in a function, in source order.
func literalText(fn *ast.FuncDecl) string {
	var parts []string
	ast.Inspect(fn, func(node ast.Node) bool {
		if literal, ok := node.(*ast.BasicLit); ok && literal.Kind.String() == "STRING" {
			parts = append(parts, literal.Value)
		}
		return true
	})
	return strings.Join(parts, "\n")
}

// callsAny reports whether the body calls any of the named functions, by simple
// name, so both e.Counts() and Counts() are seen.
func callsAny(fn *ast.FuncDecl, names ...string) bool {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	var found bool
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
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
