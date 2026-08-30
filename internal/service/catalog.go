package service

import (
	"context"
	"database/sql"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// This file covers the catalogue: customers, projects and assignments. These are
// administered rather than recorded, so they all go through ActionManage.
//
// Nothing here deletes. Archiving keeps historical entries readable and their
// invoices explicable; a deleted customer would leave last year's report unable
// to say who was billed.

// --------------------------------------------------------------- customers --

// CreateCustomer adds a customer.
func (s *Service) CreateCustomer(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "customer"}); err != nil {
		return domain.Customer{}, err
	}
	if err := c.Validate(); err != nil {
		return domain.Customer{}, err
	}
	if c.ColourKey == "" {
		c.ColourKey = "slate"
	}

	var created domain.Customer
	err := s.mutate(ctx, "customer.create", "customer", map[string]any{
		"name": c.Name, "currency": c.Currency,
	}, func(tx *sql.Tx) (int64, error) {
		var txErr error
		created, txErr = store.CreateCustomerTx(ctx, tx, c)
		return created.ID, txErr
	})
	if err != nil {
		return domain.Customer{}, err
	}
	return created, nil
}

// UpdateCustomer saves changes to a customer.
func (s *Service) UpdateCustomer(ctx context.Context, c domain.Customer) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "customer", ID: c.ID, CustomerID: c.ID,
	}); err != nil {
		return notFoundFor(err)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	return s.mutate(ctx, "customer.update", "customer",
		map[string]any{"name": c.Name}, func(tx *sql.Tx) (int64, error) {
			return c.ID, store.UpdateCustomerTx(ctx, tx, c)
		})
}

// SetCustomerArchived retires a customer from the pickers, or restores it.
func (s *Service) SetCustomerArchived(ctx context.Context, id int64, archived bool) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "customer", ID: id, CustomerID: id,
	}); err != nil {
		return notFoundFor(err)
	}
	action := "customer.restore"
	if archived {
		action = "customer.archive"
	}
	return s.mutate(ctx, action, "customer", nil, func(tx *sql.Tx) (int64, error) {
		return id, store.SetCustomerArchivedTx(ctx, tx, id, archived)
	})
}

// Customers lists the customers the actor may see.
func (s *Service) Customers(ctx context.Context, includeArchived bool) ([]domain.Customer, error) {
	if err := s.authz.Can(ctx, auth.ActionView, listResource(ctx, "customer")); err != nil {
		return nil, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	customers, err := s.db.ListCustomers(ctx, scope, includeArchived)
	if err != nil {
		return nil, err
	}
	return s.narrowCustomers(ctx, customers), nil
}

// Customer loads one customer.
func (s *Service) Customer(ctx context.Context, id int64) (domain.Customer, error) {
	c, err := s.db.GetCustomer(ctx, id)
	if err != nil {
		return domain.Customer{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "customer", ID: id, CustomerID: id,
	}); err != nil {
		return domain.Customer{}, notFoundFor(err)
	}
	return s.narrowCustomer(ctx, c), nil
}

// ---------------------------------------------------------------- projects --

// CreateProject adds a project to a customer.
func (s *Service) CreateProject(ctx context.Context, p domain.Project) (domain.Project, error) {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "project", CustomerID: p.CustomerID,
	}); err != nil {
		return domain.Project{}, err
	}
	if err := p.Validate(); err != nil {
		return domain.Project{}, err
	}
	// Confirm the customer exists before creating a child of it, so the user gets
	// a clear message rather than a foreign key error.
	if _, err := s.db.GetCustomer(ctx, p.CustomerID); err != nil {
		return domain.Project{}, err
	}
	if p.ColourKey == "" {
		p.ColourKey = "slate"
	}

	var created domain.Project
	err := s.mutate(ctx, "project.create", "project", map[string]any{
		"name": p.Name, "customer_id": p.CustomerID,
	}, func(tx *sql.Tx) (int64, error) {
		var txErr error
		created, txErr = store.CreateProjectTx(ctx, tx, p)
		return created.ID, txErr
	})
	if err != nil {
		return domain.Project{}, err
	}
	return created, nil
}

// UpdateProject saves changes to a project.
func (s *Service) UpdateProject(ctx context.Context, p domain.Project) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "project", ID: p.ID, ProjectID: p.ID, CustomerID: p.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	return s.mutate(ctx, "project.update", "project",
		map[string]any{"name": p.Name}, func(tx *sql.Tx) (int64, error) {
			return p.ID, store.UpdateProjectTx(ctx, tx, p)
		})
}

// SetProjectArchived retires a project, or restores it.
func (s *Service) SetProjectArchived(ctx context.Context, id int64, archived bool) error {
	project, err := s.db.GetProject(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "project", ID: id, ProjectID: id, CustomerID: project.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}
	action := "project.restore"
	if archived {
		action = "project.archive"
	}
	return s.mutate(ctx, action, "project", nil, func(tx *sql.Tx) (int64, error) {
		return id, store.SetProjectArchivedTx(ctx, tx, id, archived)
	})
}

// Projects lists the projects the actor may see, optionally for one customer.
func (s *Service) Projects(ctx context.Context, customerID int64, includeArchived bool) ([]domain.Project, error) {
	resource := listResource(ctx, "project")
	if customerID != 0 {
		resource.CustomerID = customerID
	}
	if err := s.authz.Can(ctx, auth.ActionView, resource); err != nil {
		return nil, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.db.ListProjects(ctx, scope, customerID, includeArchived)
	if err != nil {
		return nil, err
	}
	return s.narrowProjects(ctx, projects), nil
}

// Project loads one project.
func (s *Service) Project(ctx context.Context, id int64) (domain.Project, error) {
	p, err := s.db.GetProject(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "project", ID: id, ProjectID: id, CustomerID: p.CustomerID,
	}); err != nil {
		return domain.Project{}, notFoundFor(err)
	}
	return s.narrowProject(ctx, p), nil
}

// ------------------------------------------------------------- assignments --

// CreateAssignment adds an assignment to a project. This is the thing a timer
// runs against, so it carries the colour and icon that make the day view
// scannable.
func (s *Service) CreateAssignment(ctx context.Context, a domain.Assignment) (domain.Assignment, error) {
	project, err := s.db.GetProject(ctx, a.ProjectID)
	if err != nil {
		return domain.Assignment{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "assignment", ProjectID: a.ProjectID, CustomerID: project.CustomerID,
	}); err != nil {
		return domain.Assignment{}, err
	}
	if err := a.Validate(); err != nil {
		return domain.Assignment{}, err
	}
	// Inherit the presentation and billing defaults from the project, so a user
	// who fills in only a name gets something sensible.
	if a.ColourKey == "" {
		a.ColourKey = project.ColourKey
	}
	if a.Icon == "" {
		a.Icon = project.Icon
	}

	var created domain.Assignment
	err = s.mutate(ctx, "assignment.create", "assignment", map[string]any{
		"name": a.Name, "project_id": a.ProjectID,
	}, func(tx *sql.Tx) (int64, error) {
		var txErr error
		created, txErr = store.CreateAssignmentTx(ctx, tx, a)
		return created.ID, txErr
	})
	if err != nil {
		return domain.Assignment{}, err
	}
	return created, nil
}

// UpdateAssignment saves changes to an assignment.
func (s *Service) UpdateAssignment(ctx context.Context, a domain.Assignment) error {
	project, err := s.db.GetProject(ctx, a.ProjectID)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "assignment", ID: a.ID, ProjectID: a.ProjectID, CustomerID: project.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}
	if err := a.Validate(); err != nil {
		return err
	}
	return s.mutate(ctx, "assignment.update", "assignment",
		map[string]any{"name": a.Name}, func(tx *sql.Tx) (int64, error) {
			return a.ID, store.UpdateAssignmentTx(ctx, tx, a)
		})
}

// SetAssignmentArchived retires an assignment, or restores it.
func (s *Service) SetAssignmentArchived(ctx context.Context, id int64, archived bool) error {
	assignment, err := s.db.GetAssignment(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "assignment", ID: id, ProjectID: assignment.ProjectID, CustomerID: assignment.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}
	action := "assignment.restore"
	if archived {
		action = "assignment.archive"
	}
	return s.mutate(ctx, action, "assignment", nil, func(tx *sql.Tx) (int64, error) {
		return id, store.SetAssignmentArchivedTx(ctx, tx, id, archived)
	})
}

// Assignments lists the assignments the actor may see, optionally for one
// project.
func (s *Service) Assignments(ctx context.Context, projectID int64, includeArchived bool) ([]domain.Assignment, error) {
	resource := listResource(ctx, "assignment")
	resource.ProjectID = projectID
	if err := s.authz.Can(ctx, auth.ActionView, resource); err != nil {
		return nil, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.db.ListAssignments(ctx, scope, projectID, includeArchived)
}

// Assignment loads one assignment.
func (s *Service) Assignment(ctx context.Context, id int64) (domain.Assignment, error) {
	a, err := s.db.GetAssignment(ctx, id)
	if err != nil {
		return domain.Assignment{}, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "assignment", ID: id, ProjectID: a.ProjectID, CustomerID: a.CustomerID,
	}); err != nil {
		return domain.Assignment{}, notFoundFor(err)
	}
	return a, nil
}

// RecentWindow is how far back "lately" reaches.
//
// Six weeks, which is what the day screen already promised in words and what
// the query now actually does. It is bounded rather than open-ended for two
// reasons, and the second is the one that forced it: an assignment nobody has
// touched since last year is not a suggestion anybody wants, and grouping a
// whole history to rank eight of them cost 170 ms on every render of the day
// screen with three years of entries behind it.
const RecentWindow = 42 * 24 * time.Hour

// RecentAssignments returns what the user has worked on lately, most recent
// first. It drives the one-click start list and the quick-add matcher, both of
// which are far more useful ordered by habit than alphabetically.
func (s *Service) RecentAssignments(ctx context.Context, limit int) ([]domain.Assignment, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "assignment", OwnerID: actor.ID,
	}); err != nil {
		return nil, err
	}
	return s.db.RecentAssignments(ctx, actor.ID, s.now().Add(-RecentWindow), limit)
}

// ------------------------------------------------------------------ shared --

// mutate makes a change and records it, in one transaction.
//
// This is what ASR-006 means by an audited mutation: the change and the row
// saying who made it commit together, or neither does. The change function is
// handed the transaction and returns the id of what it touched, because a
// create only learns its id from the insert.
//
// It replaced recordAudit on these paths, and the difference is the whole
// point. recordAudit writes the audit row in a transaction of its own, *after*
// the store has already committed the change on another connection - so a
// failed audit insert returned an error to the caller with the change still
// made. The user was told their edit failed, retried, and got two customers and
// no trail for either. A test that injected a failure into the audit write
// found it; nothing else would have, because every path succeeds when nothing
// goes wrong.
//
// The log line is emitted after the commit, never inside it: a line about a
// change that was rolled back is a lie in the operational log.
func (s *Service) mutate(ctx context.Context, action, resourceType string, detail any, change func(tx *sql.Tx) (int64, error)) error {
	var resourceID int64
	err := s.db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		if resourceID, txErr = change(tx); txErr != nil {
			return txErr
		}
		return s.audit(ctx, tx, action, resourceType, resourceID, 0, detail)
	})
	if err != nil {
		return err
	}
	s.auditLog(ctx, action, resourceType, resourceID)
	return nil
}

// recordAudit writes an audit row for a change that has already been made
// through a store method.
//
// It is the older arrangement and the weaker one: the audit row lands in a
// transaction of its own, so a failure here reports failure over a change that
// stuck. The catalogue paths use mutate instead. What is left on this are the
// paths where the change is not a single database write to begin with - a
// restore that rebuilds everything, an import, an attachment whose bytes are on
// disk - and each of those needs its own answer rather than this one.
func (s *Service) recordAudit(ctx context.Context, action, resourceType string, resourceID int64, detail any) error {
	err := s.db.InTx(ctx, func(tx *sql.Tx) error {
		return s.audit(ctx, tx, action, resourceType, resourceID, 0, detail)
	})
	if err != nil {
		return err
	}
	s.auditLog(ctx, action, resourceType, resourceID)
	return nil
}
