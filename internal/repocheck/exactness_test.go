package repocheck

import (
	"go/ast"
	"strings"
	"testing"
)

// ASR-014: money and durations are exact.
//
// ADR-0014 settled the representation: money is an integer count of minor units
// and a currency, durations are whole seconds, and neither is ever a float. The
// rounding tables test the arithmetic thoroughly. What nothing tested was the
// representation itself - the thing the arithmetic rests on.
//
// It is worth a source scan rather than trust, because the mistake is not a
// deliberate decision anybody would defend. It is one field: somebody adds
// `Hours float64` to a report struct because hours is what the screen shows, the
// value passes through the exact path and out into a float at the last moment,
// and the totals are right until the day 0.1 + 0.2 appears in an invoice. Every
// existing test still passes, because every existing test is about the exact
// path.
//
// The rules below are narrow on purpose. Floating point is correct for the
// things it is used for here - a PDF's coordinates, an icon's anti-aliasing, a
// progress bar's width, an Accept-Language quality value - and a blanket ban
// would be wrong and would be worked around.

// floatTypes are the two spellings, plus the bare `float` somebody writes by
// habit.
var floatTypes = map[string]bool{"float64": true, "float32": true}

// TestPersistedValuesAreNotFloats.
//
// The domain and the store are the layers where a value is a record. A float
// field here is a float in the database, or one arithmetic step away from it.
//
// Methods returning a float are fine and are not checked: BudgetLine.UsedFraction
// returns one, and should - it is the width of a bar, computed from two integers
// that stay integers.
func TestPersistedValuesAreNotFloats(t *testing.T) {
	checked := 0

	for _, source := range goSources(t, "internal/domain", "internal/store") {
		ast.Inspect(source.file, func(node ast.Node) bool {
			structType, ok := node.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				checked++
				if name := typeName(field.Type); floatTypes[name] {
					t.Errorf("%s: %s is %s. Money is minor units and durations are "+
						"whole seconds (ADR-0014); a float here is a rounding error "+
						"waiting for an invoice.",
						source.pos(field), fieldNames(field), name)
				}
			}
			return true
		})
	}

	if checked < 100 {
		t.Errorf("only inspected %d struct fields; the scan is no longer reading the "+
			"domain and the store", checked)
	}
}

// TestTheStoreHasNoFloatingPointAtAll.
//
// A stronger rule for one package, and a defensible one: nothing the persistence
// layer does needs a fraction. It scans rows into fields, builds conditions, and
// hands values back. A float appearing anywhere in it - a local, a conversion, a
// scan destination - is either a value on its way into a column or a computation
// that belongs a layer up.
//
// Stated as a bright line rather than a list of allowed uses, because a bright
// line is the only kind of rule that survives somebody being in a hurry.
func TestTheStoreHasNoFloatingPointAtAll(t *testing.T) {
	for _, source := range goSources(t, "internal/store") {
		ast.Inspect(source.file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && floatTypes[ident.Name] {
				t.Errorf("%s mentions %s: the persistence layer deals in integers "+
					"(ADR-0014)", source.pos(ident), ident.Name)
			}
			return true
		})
	}
}

// TestQuantitiesNamedForTheirUnitAreIntegers.
//
// The rule follows the name rather than the package, and catches the case the
// two above cannot: a float in the service or the export layer, in a value that
// says in its own name what unit it is in.
//
// `AmountMinor` and `DurationSeconds` are claims. A field called Minor that is
// not a whole number of minor units is a lie a reader has no way to detect,
// which is worse than an honest `hours float64` on a PDF column - and that
// honest one is why this keys on the unit suffix rather than on words like
// amount or rate.
func TestQuantitiesNamedForTheirUnitAreIntegers(t *testing.T) {
	unitSuffixes := []string{"Minor", "Seconds", "Cents", "Millis"}
	named := func(name string) bool {
		for _, suffix := range unitSuffixes {
			if strings.HasSuffix(name, suffix) {
				return true
			}
		}
		return false
	}

	checked := 0
	for _, source := range goSources(t, "internal", "cmd") {
		ast.Inspect(source.file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok {
				return true
			}
			typeIsFloat := floatTypes[typeName(field.Type)]
			for _, name := range field.Names {
				if !named(name.Name) {
					continue
				}
				checked++
				if typeIsFloat {
					t.Errorf("%s: %s is a %s. A field named for its unit has to hold "+
						"a whole number of them (ADR-0014).",
						source.pos(field), name.Name, typeName(field.Type))
				}
			}
			return true
		})

		// The same rule for a plain declaration: `var totalSeconds float64`.
		ast.Inspect(source.file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok || spec.Type == nil || !floatTypes[typeName(spec.Type)] {
				return true
			}
			for _, name := range spec.Names {
				if named(name.Name) {
					checked++
					t.Errorf("%s: %s is a %s (ADR-0014)",
						source.pos(spec), name.Name, typeName(spec.Type))
				}
			}
			return true
		})
	}

	if checked < 20 {
		t.Errorf("only found %d values named for a unit; the scan is no longer "+
			"reading the tree", checked)
	}
}

// exemptFloatParses names the functions that may read a fraction from text, with
// the reason each is not acquiring an inexact value.
var exemptFloatParses = map[string]string{
	"ParseDuration": "reads the decimal-hours shorthand people type - \"1.5\" for " +
		"an hour and a half - and rounds it to whole seconds in the same " +
		"expression. The float never leaves the function, and refusing the form " +
		"would mean refusing what most people type first",
}

// TestMoneyIsNeverParsedAsAFloat.
//
// The other end of the same rule: however money is represented in memory, it
// arrives as text - from a form, a CSV import, a JSON body, a calendar file - and
// strconv.ParseFloat is how an exact pipeline acquires an inexact value at its
// entrance. The web package has its own version of this check at the form
// boundary; this one covers the importers, which are the other door.
func TestMoneyIsNeverParsedAsAFloat(t *testing.T) {
	for _, source := range goSources(t, "internal/service", "internal/store",
		"internal/domain", "internal/export", "internal/ical") {
		for _, decl := range source.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			if _, exempt := exemptFloatParses[fn.Name.Name]; exempt {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "ParseFloat" {
					return true
				}
				t.Errorf("%s calls ParseFloat in %s: money is parsed as minor units "+
					"and durations as whole seconds (ADR-0014). If the fraction is "+
					"genuinely user shorthand that is rounded on the spot, say so in "+
					"exemptFloatParses.", source.pos(selector), fn.Name.Name)
				return true
			})
		}
	}

	// A name in the list that no longer parses anything excuses the next
	// function given it.
	for name := range exemptFloatParses {
		found := false
		for _, source := range goSources(t, "internal/domain") {
			for _, decl := range source.file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
					found = callsAny(fn, "ParseFloat")
				}
			}
		}
		if !found {
			t.Errorf("exemptFloatParses names %s, which no longer parses a float", name)
		}
	}
}

// typeName renders a type expression as its identifier, or "" for anything more
// structured. Pointers and slices are followed, so *float64 and []float64 are
// both reported as float64.
func typeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return typeName(typed.X)
	case *ast.ArrayType:
		return typeName(typed.Elt)
	case *ast.MapType:
		return typeName(typed.Value)
	default:
		return ""
	}
}

// fieldNames renders a field's names for a failure message.
func fieldNames(field *ast.Field) string {
	var names []string
	for _, name := range field.Names {
		names = append(names, name.Name)
	}
	if len(names) == 0 {
		return "an embedded field"
	}
	return strings.Join(names, ", ")
}
