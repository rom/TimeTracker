// Package auth resolves who is making a request and decides what they may do.
//
// It exists as its own package so that the authorisation decision has exactly one
// implementation point. Handlers never decide; services ask. The two run modes
// supply different Authorizer implementations rather than different code paths,
// which means local mode is not "authorisation switched off" but a permissive
// authoriser satisfying the same interface - so the authorisation path is
// exercised in both modes and cannot be accidentally skipped in one.
//
// See docs/adr/0001-single-binary-two-modes.md and docs/adr/0008-rbac-model.md.
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/rom/timetracker/internal/domain"
)

// ErrUnauthenticated means no identity could be resolved for the request.
var ErrUnauthenticated = errors.New("not authenticated")

// ErrForbidden means an identity was resolved but is not permitted to act.
//
// Callers translate this into a "not found" response for resources the actor may
// not know exist, so that probing cannot be used to enumerate records.
var ErrForbidden = errors.New("forbidden")

// Action is a verb an actor may attempt.
type Action string

// The verbs. They are deliberately about records rather than about screens:
// "may this actor update this entry" is a question with one answer, whereas "may
// they open the edit page" has as many answers as there are ways to reach the
// page.
const (
	ActionView   Action = "view"   // read one record or a listing
	ActionCreate Action = "create" // add a record
	ActionUpdate Action = "update" // change an existing one
	ActionDelete Action = "delete" // remove one
	// ActionManage covers catalogue and settings administration.
	ActionManage Action = "manage"
	// ActionViewMoney gates rates and amounts, which not every role may see.
	ActionViewMoney Action = "view_money"
	// ActionProxy is recording time on behalf of another user. It only ever
	// creates a pending proposal; it never confirms anything.
	ActionProxy Action = "proxy"
	// ActionApprove is deciding on submitted timesheets.
	ActionApprove Action = "approve"
)

// Resource describes what is being acted upon. Only the fields relevant to the
// decision are populated by callers.
type Resource struct {
	// Type is "time_entry", "customer", "project", "assignment", "settings", ...
	Type string
	// ID is the record's identifier, 0 for a not-yet-created record.
	ID int64
	// OwnerID is the user the record belongs to, where that concept applies.
	// For a time entry this is whose time it is, not who typed it in.
	OwnerID int64
	// CustomerID and ProjectID place the resource in the hierarchy, which is the
	// dimension project membership scopes in server mode.
	CustomerID int64
	ProjectID  int64
}

// Authorizer answers whether an actor may perform an action.
//
// Implementations must be safe for concurrent use and must not consult the run
// mode: the mode chooses the implementation once, at startup.
type Authorizer interface {
	// Can returns nil if the action is permitted, ErrForbidden if it is not, or
	// ErrUnauthenticated if there is no identity in the context.
	Can(ctx context.Context, action Action, resource Resource) error
}

// contextKey is unexported so no other package can plant an identity in the
// context. An actor can only get there through the middleware in internal/web.
type contextKey struct{}

var userKey contextKey

// WithUser returns a context carrying the acting user. Called once per request by
// the identity middleware, and by nothing else.
func WithUser(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom extracts the acting user. The second return is false when the request
// is unauthenticated.
func UserFrom(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userKey).(domain.User)
	return u, ok
}

// MustUser returns the acting user or an error, for service methods that cannot
// proceed without one.
func MustUser(ctx context.Context) (domain.User, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return domain.User{}, ErrUnauthenticated
	}
	return u, nil
}

// SingleUserAuthorizer is the local-mode implementation: the one person using the
// application owns everything in it.
//
// It is not a no-op. It still requires an identity in the context, and it still
// refuses to let the single user act on another user's records - a rule that has
// no effect while there is one user, and which means the same service tests can
// run against both authorisers.
type SingleUserAuthorizer struct{}

// Can implements Authorizer for local mode.
func (SingleUserAuthorizer) Can(ctx context.Context, action Action, resource Resource) error {
	actor, err := MustUser(ctx)
	if err != nil {
		return err
	}
	// Records owned by somebody else are refused even here, so that a local
	// database later opened in server mode cannot have been built on the
	// assumption that ownership does not matter.
	if resource.OwnerID != 0 && resource.OwnerID != actor.ID {
		return fmt.Errorf("%w: %s on %s owned by another user", ErrForbidden, action, resource.Type)
	}
	// Approval is refused outright in local mode. There is one person, and
	// approving your own timesheet is not approval - so the control is simply
	// absent rather than present and meaningless.
	if action == ActionApprove {
		return fmt.Errorf("%w: there is nobody else here to approve a timesheet", ErrForbidden)
	}
	return nil
}

// RoleAuthorizer is the server-mode implementation of the four-role model in
// docs/adr/0008-rbac-model.md, scoped by project membership.
//
// The membership lookup is injected rather than queried here, so this package
// stays free of database dependencies and the decision table can be tested
// exhaustively without fixtures.
type RoleAuthorizer struct {
	// IsProjectMember reports whether a user belongs to a project. It is called
	// only for decisions that need scoping.
	IsProjectMember func(ctx context.Context, userID, projectID int64) (bool, error)
	// IsCustomerMember reports whether a user belongs to any project of a
	// customer. A customer record has no single project, so membership of one
	// is the nearest true answer to "may this person read this account": the
	// alternative was permitting it outright, which gave every member every
	// customer's negotiated rate.
	IsCustomerMember func(ctx context.Context, userID, customerID int64) (bool, error)
}

// scopedTypes are the resource types whose records belong to a customer, and
// usually to a project within it.
//
// A resource naming one of these records - an id rather than a listing - must
// also say where in the hierarchy it sits, or there is nothing to scope it by.
// It is a deny list by construction: a type absent from here is administrative
// (settings, a user account, a backup) and is gated by role alone, which is the
// decision the role branches above have already made by the time scoping is
// asked about.
//
// This exists because the permissive branch it guards was reached by accident.
// requireMembership permits a resource with no project, which is right for a
// global setting - and entryResource built a time entry without one, so a
// member asking for another member's entry by id was answered as though they
// had asked about the instance settings. The fix was to fill the field in; this
// is what makes the next omission fail closed instead.
var scopedTypes = map[string]bool{
	"time_entry": true, "expense": true, "assignment": true,
	"project": true, "customer": true, "contract_terms": true,
	"attachment": true,
}

// Can implements Authorizer for server mode.
//
// The structure is deliberately a flat, readable decision table rather than a
// clever rule engine: this is the security policy, and it must be obvious.
func (a RoleAuthorizer) Can(ctx context.Context, action Action, resource Resource) error {
	actor, err := MustUser(ctx)
	if err != nil {
		return err
	}
	if !actor.Active {
		return fmt.Errorf("%w: account is not active", ErrForbidden)
	}

	deny := func(reason string) error {
		return fmt.Errorf("%w: %s may not %s %s (%s)", ErrForbidden, actor.Role, action, resource.Type, reason)
	}

	switch actor.Role {
	case domain.RoleAdmin:
		// Admins may do anything. This is the only unconditional branch.
		return nil

	case domain.RoleManager:
		switch action {
		case ActionManage, ActionApprove, ActionProxy, ActionViewMoney,
			ActionView, ActionCreate, ActionUpdate, ActionDelete:
			// A manager's authority is real but bounded by membership: they act
			// on the projects they are responsible for, not on every project.
			return a.requireMembership(ctx, actor.ID, resource, deny)
		default:
			return deny("unknown action")
		}

	case domain.RoleMember:
		switch action {
		case ActionView:
			// Members read their own records and the projects they belong to.
			if resource.OwnerID == actor.ID {
				return nil
			}
			return a.requireMembership(ctx, actor.ID, resource, deny)
		case ActionCreate, ActionUpdate, ActionDelete:
			// Write access is limited to their own time.
			if resource.OwnerID == 0 || resource.OwnerID == actor.ID {
				return nil
			}
			return deny("members may only change their own records")
		case ActionProxy:
			// Proposing time for a colleague is allowed on a shared project. The
			// proposal still requires the colleague's confirmation before it
			// counts, which is what makes this safe.
			// See docs/adr/0005-proxy-time-entry.md.
			return a.requireMembership(ctx, actor.ID, resource, deny)
		case ActionViewMoney:
			if resource.OwnerID == actor.ID {
				return nil
			}
			return deny("members see only their own rates")
		default:
			return deny("members may not administer or approve")
		}

	case domain.RoleClient:
		// Clients get read-only access to their own customer, and only to
		// confirmed data. The narrowed projection is built in the service layer
		// so that internal notes and cost data never leave it at all.
		if action != ActionView {
			return deny("client access is read-only")
		}
		if resource.CustomerID == 0 {
			return deny("client access is scoped to a customer")
		}
		// Their own customer, asked here rather than trusted from the caller.
		// Every call site passes the acting client's own id today, which is the
		// only reason "any customer" was ever safe - and a check that is
		// correct because of what its callers happen to pass is one call site
		// away from being wrong.
		if resource.CustomerID != actor.ClientCustomerID {
			return deny("a client reads their own customer and no other")
		}
		return nil

	default:
		return deny("unknown role")
	}
}

// requireMembership permits the action when the actor belongs to the resource's
// project, or - for a customer, which has no single project - to any project of
// that customer.
//
// A resource with neither (a global setting, a new record, a listing that the
// service narrows with effectiveScope instead) is permitted, because the caller
// has already established the actor's role allows it in principle. The one
// exception is a named record of a scopedType, which must say where it sits:
// see the note there.
func (a RoleAuthorizer) requireMembership(ctx context.Context, userID int64, resource Resource, deny func(string) error) error {
	switch {
	case resource.ProjectID != 0:
		if a.IsProjectMember == nil {
			// Failing closed: a misconfigured authoriser must refuse, never
			// allow.
			return deny("membership lookup unavailable")
		}
		member, err := a.IsProjectMember(ctx, userID, resource.ProjectID)
		if err != nil {
			return fmt.Errorf("check project membership: %w", err)
		}
		if !member {
			return deny("not a member of the project")
		}
		return nil

	case resource.CustomerID != 0:
		if a.IsCustomerMember == nil {
			return deny("membership lookup unavailable")
		}
		member, err := a.IsCustomerMember(ctx, userID, resource.CustomerID)
		if err != nil {
			return fmt.Errorf("check customer membership: %w", err)
		}
		if !member {
			return deny("not a member of any of the customer's projects")
		}
		return nil

	case resource.ID != 0 && scopedTypes[resource.Type]:
		return deny("the resource names no project or customer to scope it by")

	default:
		return nil
	}
}
