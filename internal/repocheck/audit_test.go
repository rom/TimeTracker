package repocheck

import (
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ASR-006: every mutation leaves an audit record, and the record is append-only.
//
// The first half is tested by running code: TestAuditTrailRecordsEveryMutation
// walks the mutating service methods and asserts each leaves a row, written in
// the same transaction as the change. The second half cannot be tested that way.
// "No code path updates or deletes an audit row" is a statement about statements
// that do not exist, and the only way to check it is to read every statement
// that does.
//
// It matters more than it sounds. An audit trail that can be edited is not an
// audit trail; it is a log with an undo. The dangerous version is not somebody
// maliciously rewriting history - it is a well-meant cleanup, a cascade on a
// foreign key, or a "fix the actor name on those rows" migration, any of which
// makes the whole table unciteable afterwards because nobody can say which rows
// were touched.

// auditTable is the append-only table. Named once so a rename fails loudly here
// rather than quietly turning every check below into a no-op.
const auditTable = "audit_events"

// TestTheAuditTableExists.
//
// The scans below all key on a table name. If the table were renamed, every one
// of them would find nothing to object to and pass, which is the failure mode a
// source scan must never have.
func TestTheAuditTableExists(t *testing.T) {
	var found bool
	for _, path := range filesMatching(t, "internal/store/migrations", ".sql") {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(content), "CREATE TABLE "+auditTable) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no migration creates %s: the immutability scans below would all "+
			"pass by finding nothing", auditTable)
	}
}

// mutatesAudit matches a statement that changes or removes existing audit rows.
//
// Written as three separate patterns rather than one clever one, because each
// has a different shape and a regexp that tried to cover all three would match
// prose in a comment.
var mutatesAudit = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"UPDATE", regexp.MustCompile(`(?i)\bUPDATE\s+` + auditTable + `\b`)},
	{"DELETE", regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+` + auditTable + `\b`)},
	{"DROP", regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(IF\s+EXISTS\s+)?` + auditTable + `\b`)},
}

// TestNoMigrationRewritesTheAuditTrail.
//
// Migrations are the likeliest place for this: they run with no user watching,
// they are written to fix data, and a forward-only migration that deleted audit
// rows would be unrecoverable by design.
//
// Adding a column is allowed. Widening the record is not rewriting it, and
// refusing an ALTER would mean the audit table could never gain a field.
func TestNoMigrationRewritesTheAuditTrail(t *testing.T) {
	for _, path := range filesMatching(t, "internal/store/migrations", ".sql") {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		name := filepath.Base(path)
		sql := stripSQLComments(string(content))
		for _, statement := range mutatesAudit {
			if statement.pattern.MatchString(sql) {
				t.Errorf("%s issues %s against %s. The audit trail is append-only "+
					"(ASR-006): a row that can be edited cannot be cited.",
					name, statement.name, auditTable)
			}
		}
		// A foreign key with a cascade is the same thing written indirectly:
		// deleting a user would take their audit history with it, which is
		// precisely the history somebody would want.
		if strings.Contains(sql, "CREATE TABLE "+auditTable) {
			table := sql[strings.Index(sql, "CREATE TABLE "+auditTable):]
			if end := strings.Index(table, ";"); end > 0 {
				table = table[:end]
			}
			if regexp.MustCompile(`(?i)ON\s+DELETE\s+CASCADE`).MatchString(table) {
				t.Errorf("%s: %s cascades on delete, so removing a user erases their "+
					"audit history", name, auditTable)
			}
		}
	}
}

// TestNoGoCodeRewritesTheAuditTrail.
//
// The same rule against the queries in internal/store. Everything reaching the
// database goes through that package, so a statement not written here cannot be
// executed - which makes this scan complete rather than a sample.
func TestNoGoCodeRewritesTheAuditTrail(t *testing.T) {
	for _, source := range goSources(t, "internal") {
		ast.Inspect(source.file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind.String() != "STRING" {
				return true
			}
			for _, statement := range mutatesAudit {
				if statement.pattern.MatchString(literal.Value) {
					t.Errorf("%s issues %s against %s. The audit trail is append-only "+
						"(ASR-006).", source.pos(literal), statement.name, auditTable)
				}
			}
			return true
		})
	}
}

// TestTheAuditTrailIsWrittenInsideTheTransaction.
//
// The insert takes a *sql.Tx rather than the database handle, and that is the
// whole of the "same transaction as the change" guarantee: a function that could
// be handed a plain connection could write a row for a change that later rolled
// back, leaving the trail claiming something happened that did not.
//
// Asserted on the signature because it is the signature that enforces it. A test
// that merely observed a row after a successful mutation would pass just as well
// with two separate transactions.
func TestTheAuditTrailIsWrittenInsideTheTransaction(t *testing.T) {
	var checked bool
	for _, source := range goSources(t, "internal/store") {
		for _, decl := range source.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || !strings.Contains(fn.Name.Name, "Audit") {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Insert") {
				continue
			}
			checked = true
			if !takesTransaction(fn) {
				t.Errorf("%s does not take a *sql.Tx, so an audit row could be written "+
					"outside the transaction that made the change (ASR-006)", fn.Name.Name)
			}
		}
	}
	if !checked {
		t.Error("found no audit insert to check; it has been renamed and this test " +
			"is now watching nothing")
	}
}

// takesTransaction reports whether any parameter is a *sql.Tx.
func takesTransaction(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	var found bool
	for _, param := range fn.Type.Params.List {
		ast.Inspect(param.Type, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "sql" && selector.Sel.Name == "Tx" {
				found = true
			}
			return true
		})
	}
	return found
}

// stripSQLComments removes -- line comments so that prose mentioning DELETE does
// not fail a scan looking for statements.
func stripSQLComments(sql string) string {
	var kept []string
	for _, line := range strings.Split(sql, "\n") {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
