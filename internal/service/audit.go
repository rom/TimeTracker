package service

import (
	"context"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/store"
)

// Reading the audit trail.
//
// Every mutation has written a row since the first migration, in the same
// transaction as the change, and until now nothing in the application read
// one. The data was impeccable and unreachable: the only query was for a
// single record's history, the only service method that called it had no
// caller outside the tests, and no screen, export or backup touched it.
//
// In server mode an operator could read the mirror in syslog. The person
// looking at a corrected entry and wondering who corrected it could not, and in
// local mode - where nothing is forwarded anywhere - neither could anybody.
// ASR-006 promises that every mutating action is attributable; writing it down
// is half of that.
//
// Two readers, because there are two questions. "Who changed this" is asked of
// a record and is answered beside it, bounded by whoever may read that record.
// "What happened here" is asked of the instance and is administration, like the
// settings and the accounts: it crosses every project and names people whose
// work the reader may have no business seeing.

// AuditEvent is one row of the trail, re-exported for the HTTP layer.
//
// The alias rather than the store type directly, because a handler importing
// internal/store is refused by the layering test - and rightly: that is how a
// request reaches the database without passing an authorisation check.
type AuditEvent = store.AuditEvent

// AuditQuery narrows the instance-wide trail.
type AuditQuery struct {
	ActorID      int64
	Action       string
	ResourceType string
	// From and To are days, inclusive at both ends as a person reads them;
	// the service turns the end into the exclusive instant the store wants.
	From string
	To   string
	Page int
}

// AuditPage is one screen of the trail.
type AuditPage struct {
	Events []store.AuditEvent
	// Actions and Actors are what the filter offers: what the trail actually
	// holds, rather than every action the code can write.
	Actions []string
	Actors  []AuditActor
	Query   AuditQuery
	Page    int
	// HasMore says whether to offer the next page. The count is deliberately
	// not computed: this is the largest table in the schema, nothing prunes
	// it, and a total that costs a full scan to show is a worse answer than
	// "there is more".
	HasMore bool
}

// AuditActor is somebody who appears in the trail.
type AuditActor struct {
	ID   int64
	Name string
}

// auditPageSize is how many rows a page of the trail shows.
const auditPageSize = 100

// AuditLog returns the instance's history.
//
// Administration, and only administration. The trail crosses every project and
// every person: a manager bounded to their own customers would still read, in
// this one listing, who deleted what on somebody else's. That is the same
// reasoning that keeps the user list and the instance settings out of a
// manager's reach (docs/adr/0008-rbac-model.md).
func (s *Service) AuditLog(ctx context.Context, query AuditQuery) (AuditPage, error) {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "settings"}); err != nil {
		return AuditPage{}, notFoundFor(err)
	}

	page := query.Page
	if page < 1 {
		page = 1
	}

	filter := store.AuditFilter{
		ActorID:      query.ActorID,
		Action:       query.Action,
		ResourceType: query.ResourceType,
		// One more than a page, so "is there another page" is answered
		// without counting the table.
		Limit:  auditPageSize + 1,
		Offset: (page - 1) * auditPageSize,
	}
	if from, err := time.Parse("2006-01-02", query.From); err == nil {
		filter.From = from
	}
	if to, err := time.Parse("2006-01-02", query.To); err == nil {
		// Inclusive on the form, exclusive in the store: somebody asking for
		// the 31st means the whole of it.
		filter.To = to.AddDate(0, 0, 1)
	}

	events, err := s.db.ListAuditTrail(ctx, filter)
	if err != nil {
		return AuditPage{}, err
	}

	result := AuditPage{Query: query, Page: page}
	if len(events) > auditPageSize {
		result.HasMore = true
		events = events[:auditPageSize]
	}
	result.Events = events

	if result.Actions, err = s.db.AuditActions(ctx); err != nil {
		return AuditPage{}, err
	}
	users, err := s.db.ListUsers(ctx)
	if err != nil {
		return AuditPage{}, err
	}
	for _, user := range users {
		result.Actors = append(result.Actors, AuditActor{ID: user.ID, Name: user.DisplayName})
	}
	return result, nil
}
