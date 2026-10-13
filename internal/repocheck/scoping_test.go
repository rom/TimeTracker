package repocheck

import (
	"go/ast"
	"strconv"
	"testing"
)

// ASR-005: every read of a record is scoped by project membership.
//
// The authorisation decision is one function, and it decides from the resource
// it is handed. That makes a resource literal part of the security policy: a
// read that names a record without saying where that record sits in the
// hierarchy is asking a question with no scope in it.
//
// For three reads it did. `requireMembership` permits a resource with no
// project - correctly, for a global setting or a listing the service narrows
// itself - and `entryResource` built a time entry without one, so "may this
// member read this entry" was answered as though it had been asked about the
// instance settings. Another user's entry came back with its note and its
// billing snapshot. The authoriser now refuses such a resource instead, so the
// mistake fails closed rather than open.
//
// This is the other half: failing closed turns a disclosure into a refusal, and
// a refusal somewhere no test looks is still a bug - it just breaks a manager
// instead of leaking to a member. So the literals are checked here, where every
// call site is visible at once rather than only the ones a test happens to
// exercise.

// scopedResourceTypes mirrors auth.scopedTypes.
//
// Duplicated deliberately, and the test below fails if the two disagree: this
// scan reads source text and cannot import the map without the rule and its
// enforcement becoming the same statement, which is how a guard stops guarding.
var scopedResourceTypes = map[string]bool{
	"time_entry": true, "expense": true, "assignment": true,
	"project": true, "customer": true, "contract_terms": true,
	"attachment": true,
}

// TestEveryNamedRecordSaysWhereItSits.
func TestEveryNamedRecordSaysWhereItSits(t *testing.T) {
	checked := 0

	for _, source := range goSources(t, "internal/service", "internal/web") {
		ast.Inspect(source.file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok || !isAuthResource(literal.Type) {
				return true
			}
			fields := resourceFields(literal)
			resourceType, named := fields["Type"]
			if !named || !scopedResourceTypes[resourceType] {
				return true
			}
			checked++

			// A listing carries no id and is narrowed by effectiveScope after
			// the check, which is a different mechanism and a correct one.
			if _, hasID := fields["ID"]; !hasID {
				return true
			}
			_, hasProject := fields["ProjectID"]
			_, hasCustomer := fields["CustomerID"]
			if !hasProject && !hasCustomer {
				t.Errorf("%s: a %s is named by id and says nothing about which "+
					"project or customer it belongs to.\n"+
					"The authoriser refuses that rather than permitting it "+
					"(ASR-005, ADR-0008), so this read would fail for anybody "+
					"but the record's owner. Name the project, or the customer "+
					"for a record that has no single project.",
					source.pos(literal), resourceType)
			}
			return true
		})
	}

	if checked < 20 {
		t.Errorf("only %d scoped resource literals were found; the scan is no "+
			"longer reading the code", checked)
	}
}

// TestTheScanAndTheAuthoriserAgreeOnWhatIsScoped.
//
// The list above is a copy of the authoriser's, and a copy that drifts is worse
// than no copy: a type added there and not here is a type this scan stops
// checking, silently.
func TestTheScanAndTheAuthoriserAgreeOnWhatIsScoped(t *testing.T) {
	var found map[string]bool

	for _, source := range goSources(t, "internal/auth") {
		ast.Inspect(source.file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok || len(spec.Names) == 0 || spec.Names[0].Name != "scopedTypes" {
				return true
			}
			found = map[string]bool{}
			for _, value := range spec.Values {
				literal, isLiteral := value.(*ast.CompositeLit)
				if !isLiteral {
					continue
				}
				for _, element := range literal.Elts {
					pair, isPair := element.(*ast.KeyValueExpr)
					if !isPair {
						continue
					}
					if key, err := stringValue(pair.Key); err == nil {
						found[key] = true
					}
				}
			}
			return false
		})
	}

	if found == nil {
		t.Fatal("auth.scopedTypes was not found; if it was renamed, rename the " +
			"copy in this file with it")
	}
	for name := range found {
		if !scopedResourceTypes[name] {
			t.Errorf("auth.scopedTypes has %q and this file's copy does not, so "+
				"the scan above is not checking it", name)
		}
	}
	for name := range scopedResourceTypes {
		if !found[name] {
			t.Errorf("this file's copy has %q and auth.scopedTypes does not, so "+
				"the scan above enforces a rule the authoriser does not", name)
		}
	}
}

// isAuthResource reports whether a composite literal's type is auth.Resource.
func isAuthResource(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || selector.Sel.Name != "Resource" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "auth"
}

// resourceFields returns the fields a resource literal sets, with the string
// value of Type where it is a literal one.
func resourceFields(literal *ast.CompositeLit) map[string]string {
	fields := map[string]string{}
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok {
			continue
		}
		value := ""
		if literalValue, err := stringValue(pair.Value); err == nil {
			value = literalValue
		}
		fields[key.Name] = value
	}
	return fields
}

// stringValue unquotes a string literal.
func stringValue(expr ast.Expr) (string, error) {
	basic, ok := expr.(*ast.BasicLit)
	if !ok {
		return "", errNotAString
	}
	return strconv.Unquote(basic.Value)
}

// errNotAString is returned for an expression that is not a string literal - a
// variable, a constant, a call. The scans here read what the source says and
// make no attempt to evaluate it.
var errNotAString = errStringf("not a string literal")

// errStringf is a tiny error constructor, so this file needs no fmt.
type errStringf string

func (e errStringf) Error() string { return string(e) }
