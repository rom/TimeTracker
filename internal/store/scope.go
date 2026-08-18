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
	// OwnProjects, when set with ProjectIDs, is used for a member who may also
	// see their own records regardless of project membership.
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
func (s Scope) condition(projectCol, customerCol string) (string, []any) {
	if s.Unrestricted {
		return "", nil
	}

	var clauses []string
	var args []any

	if customerCol != "" && s.CustomerID != 0 {
		clauses = append(clauses, customerCol+" = ?")
		args = append(args, s.CustomerID)
	}

	if projectCol != "" && s.CustomerID == 0 {
		if len(s.ProjectIDs) == 0 {
			// No memberships and no customer: the actor sees nothing. Returning
			// an always-false condition is deliberate - the alternative, an
			// empty clause, would silently mean "everything".
			return "1 = 0", nil
		}
		// The placeholder list is built from the count of ids, never from their
		// values, so this stays fully parameterised.
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(s.ProjectIDs)), ",")
		clauses = append(clauses, projectCol+" IN ("+placeholders+")")
		for _, id := range s.ProjectIDs {
			args = append(args, id)
		}
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " AND "), args
}
