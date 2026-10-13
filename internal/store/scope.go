package store

import "strings"

// Scope is the set of records an actor is allowed to see.
//
// Every listing query takes one. The point is that there is no unscoped list to
// call by accident: a query that returns everything and then relies on the
// template to hide rows is one template bug away from showing a manager another
// engagement's timesheet.
//
// The scope is derived from the actor's role and project memberships in the
// service layer; this package only knows how to turn it into a WHERE clause.
// See docs/adr/0008-rbac-model.md.
type Scope struct {
	// Unrestricted lifts every restriction. Only an administrator gets this, and
	// only the service layer can construct it.
	Unrestricted bool
	// ProjectIDs limits results to these projects. An empty slice with
	// Unrestricted false and no CustomerID means "nothing", which is the correct
	// reading for a user who belongs to no projects - not "everything".
	ProjectIDs []int64
	// CustomerID limits results to one customer, for a client user.
	CustomerID int64
	// OwnUserID is the member who may also see their own records regardless of
	// project membership. It widens a listing only where the caller names an
	// owner column: see condition.
	OwnUserID int64
}

// UnrestrictedScope is the administrator's view.
func UnrestrictedScope() Scope { return Scope{Unrestricted: true} }

// condition renders the scope as a SQL fragment plus its bound arguments.
//
// projectCol and customerCol name the columns to compare in the caller's query,
// so the same scope works across the different joins each listing uses. Both may
// be empty when a table has no such column, in which case that dimension is not
// constrained.
//
// ownerCol is the third dimension and the one that widens rather than narrows:
// named, a record belonging to the actor is in scope whether or not they belong
// to its project. Every caller states it explicitly, including as "" - because
// the two answers are both right in different places, and the difference is not
// obvious from the query. A member's own timesheet must include the hour they
// recorded on a project they are not a member of; a project's *budget* must not
// appear to them because of that hour.
//
// It was the missing half of a promise: scopeFor fills OwnUserID and documents
// "member - the projects they are a member of, plus their own records", and this
// function ignored it. A member with no memberships at all - which ADR-0008 says
// is what every new and every SSO-provisioned account starts as, seeing "their
// own (empty) timesheet" - could record time and then find every screen empty,
// because the empty project list renders as "1 = 0" and nothing re-admitted
// their own rows.
func (s Scope) condition(projectCol, customerCol, ownerCol string) (string, []any) {
	if s.Unrestricted {
		return "", nil
	}

	var clauses []string
	var args []any

	if customerCol != "" && s.CustomerID != 0 {
		clauses = append(clauses, customerCol+" = ?")
		args = append(args, s.CustomerID)
	}

	// Own records, where the caller says the table has an owner. A client is
	// excluded on purpose: their scope is a customer, and the work they read is
	// by definition somebody else's.
	own := ""
	var ownArgs []any
	if ownerCol != "" && s.OwnUserID != 0 && s.CustomerID == 0 {
		own = ownerCol + " = ?"
		ownArgs = []any{s.OwnUserID}
	}

	if projectCol != "" && s.CustomerID == 0 {
		if len(s.ProjectIDs) == 0 {
			// No memberships and no customer: the actor sees their own records
			// and nothing else. Without an owner column that is nothing at all,
			// and an always-false condition is deliberate - the alternative, an
			// empty clause, would silently mean "everything".
			if own == "" {
				return "1 = 0", nil
			}
			return own, ownArgs
		}
		// The placeholder list is built from the count of ids, never from their
		// values, so this stays fully parameterised.
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(s.ProjectIDs)), ",")
		membership := projectCol + " IN (" + placeholders + ")"
		for _, id := range s.ProjectIDs {
			args = append(args, id)
		}
		if own != "" {
			// Either, so the parenthesis matters: this clause is ANDed with
			// every other condition in the query.
			clauses = append(clauses, "("+membership+" OR "+own+")")
			args = append(args, ownArgs...)
		} else {
			clauses = append(clauses, membership)
		}
	} else if own != "" && projectCol == "" {
		// A table with an owner and no project: the owner is the whole scope.
		clauses = append(clauses, own)
		args = append(args, ownArgs...)
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " AND "), args
}
