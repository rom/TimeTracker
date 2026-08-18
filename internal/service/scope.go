package service

import (
	"context"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// scopeFor derives what the acting user may see, from their role and their
// project memberships.
//
// Every listing method calls this and passes the result down to the store, so a
// query cannot accidentally return records outside the actor's reach. The rules:
//
//	admin    everything
//	manager  the projects they are a member of
//	member   the projects they are a member of, plus their own records
//	client   their own customer, and nothing else
//
// A user with no memberships gets an empty scope, which the store renders as
// "match nothing" rather than "no restriction" - the direction that fails safe.
// See docs/adr/0008-rbac-model.md.
func (s *Service) scopeFor(ctx context.Context) (store.Scope, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return store.Scope{}, err
	}

	switch actor.Role {
	case domain.RoleAdmin:
		return store.UnrestrictedScope(), nil

	case domain.RoleClient:
		// A client sees exactly one customer. If none is set the scope stays
		// empty, so they see nothing at all - which is the right answer for a
		// misconfigured client account.
		return store.Scope{CustomerID: actor.ClientCustomerID}, nil

	case domain.RoleManager, domain.RoleMember:
		projectIDs, err := s.db.ProjectIDsForUser(ctx, actor.ID)
		if err != nil {
			return store.Scope{}, err
		}
		return store.Scope{ProjectIDs: projectIDs, OwnUserID: actor.ID}, nil

	default:
		// An unknown role sees nothing. Failing closed matters more here than
		// producing a helpful message.
		return store.Scope{}, nil
	}
}

// listResource describes a *listing* for an authorisation decision.
//
// A list has no single record to name, so the resource is filled from the
// actor's own boundary: a client's request to list customers is a request about
// their own customer, and saying so lets the authoriser make its ordinary
// decision instead of needing a special case for "no id supplied".
//
// The scope applied to the query afterwards is what actually narrows the rows;
// this only settles whether the actor may ask the question at all.
func listResource(ctx context.Context, resourceType string) auth.Resource {
	resource := auth.Resource{Type: resourceType}
	if actor, ok := auth.UserFrom(ctx); ok && actor.Role == domain.RoleClient {
		resource.CustomerID = actor.ClientCustomerID
	}
	return resource
}

// effectiveScope returns the scope to use for a listing.
//
// Local mode has one user and no memberships table to consult, so consulting it
// would make every list empty. The mode is not inspected here: the local
// authoriser is the permissive one, and a user carrying the admin role - which
// the single local user does - already resolves to the unrestricted scope
// through the ordinary path above.
func (s *Service) effectiveScope(ctx context.Context) (store.Scope, error) {
	return s.scopeFor(ctx)
}
