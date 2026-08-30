package service

import (
	"context"
	"iter"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// The client projection (ADR-0008).
//
// A client sees their own customer's completed work and nothing else. Two rules
// make that true, and they are separate on purpose:
//
//   - what is fetched: confirmed, unflagged entries for one customer. Enforced
//     in the query, so nothing provisional is ever read.
//   - what is returned: the narrowed shape, with notes, money, proxy authorship,
//     tags and attachment counts removed. Enforced on the value, so nothing
//     downstream - a template, an export writer, a JSON encoder - can render
//     what it was never given.
//
// The second is the one ADR-0008 committed to, and the reason it is a
// transformation of the value rather than a condition in a template: a template
// that forgets a condition renders a note, and a struct that never held the note
// cannot.

// actingAsClient reports whether the request is being made by a client user.
//
// Errors are treated as "not a client", which is safe in the direction that
// matters: the narrowing is applied on top of an authorisation check that has
// already refused anybody who should not be reading at all, so the only effect
// of getting this wrong is that somebody who is not a client keeps their notes.
func actingAsClient(ctx context.Context) bool {
	actor, ok := auth.UserFrom(ctx)
	return ok && actor.Role == domain.RoleClient
}

// clientResource describes a listing the way the authoriser needs to see it for
// a client.
//
// A client's permission is defined by their customer, and a listing has no
// single record to name - so without this the authoriser is asked "may this
// client view an entry belonging to nobody in particular", which it correctly
// refuses. It is the same reasoning as listResource, applied to the entry reads
// that had been left out: before this, a client could sign in and read nothing
// at all, which is not a narrowed projection so much as an absent one.
func clientResource(ctx context.Context, resourceType string) auth.Resource {
	resource := auth.Resource{Type: resourceType}
	if actor, ok := auth.UserFrom(ctx); ok {
		resource.OwnerID = actor.ID
		if actor.Role == domain.RoleClient {
			resource.CustomerID = actor.ClientCustomerID
			// A client is not the owner of the work they are reading, and
			// claiming to be would let the ownership branch answer instead of
			// the customer one.
			resource.OwnerID = 0
		}
	}
	return resource
}

// narrowFilter restricts what a client's query may return.
//
// Applied to the store filter rather than to the results: an entry a client may
// not see should not be read, counted, or paged over. It is what makes "page 2
// of 3" mean the same thing to them as the rows they can see.
func narrowFilter(ctx context.Context, filter EntryFilter) EntryFilter {
	if !actingAsClient(ctx) {
		return filter
	}
	// A proposal awaiting somebody's confirmation, and an entry flagged for
	// review, are not work anybody has agreed happened. A client asking what was
	// done for them this month must not be shown either.
	filter.CountingOnly = true
	return filter
}

// narrowEntries applies the projection to a listing.
func (s *Service) narrowEntries(ctx context.Context, entries []domain.TimeEntry) []domain.TimeEntry {
	if !actingAsClient(ctx) {
		return entries
	}
	return domain.ProjectEntriesForClient(entries)
}

// narrowEntry applies the projection to one entry.
func (s *Service) narrowEntry(ctx context.Context, entry domain.TimeEntry) domain.TimeEntry {
	if !actingAsClient(ctx) {
		return entry
	}
	return entry.ForClient()
}

// narrowStream applies the projection to a streamed export.
//
// The stream is where forgetting would be least visible: an export runs without
// anybody reading it on screen first, and the file is the thing that gets
// forwarded. Wrapping the sequence rather than narrowing at each yield site
// keeps it one decision.
func (s *Service) narrowStream(ctx context.Context, entries iter.Seq2[domain.TimeEntry, error]) iter.Seq2[domain.TimeEntry, error] {
	if !actingAsClient(ctx) {
		return entries
	}
	return func(yield func(domain.TimeEntry, error) bool) {
		for entry, err := range entries {
			if err != nil {
				yield(domain.TimeEntry{}, err)
				return
			}
			if !yield(entry.ForClient(), nil) {
				return
			}
		}
	}
}

// narrowCustomers and narrowProjects apply the projection to the catalogue.
//
// The catalogue is where this leaked first: a client opening the administration
// screen saw their own customer's negotiated hourly rate. The names are what a
// client needs to read a report; the rates, notes and budgets are what the role
// exists to withhold.
func (s *Service) narrowCustomers(ctx context.Context, customers []domain.Customer) []domain.Customer {
	if !actingAsClient(ctx) {
		return customers
	}
	return domain.ProjectCustomersForClient(customers)
}

func (s *Service) narrowCustomer(ctx context.Context, customer domain.Customer) domain.Customer {
	if !actingAsClient(ctx) {
		return customer
	}
	return customer.ForClient()
}

func (s *Service) narrowProjects(ctx context.Context, projects []domain.Project) []domain.Project {
	if !actingAsClient(ctx) {
		return projects
	}
	return domain.ProjectProjectsForClient(projects)
}

func (s *Service) narrowProject(ctx context.Context, project domain.Project) domain.Project {
	if !actingAsClient(ctx) {
		return project
	}
	return project.ForClient()
}
