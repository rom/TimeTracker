package service

import (
	"context"
	"database/sql"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
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

	created, err := s.db.CreateCustomer(ctx, c)
	if err != nil {
		return domain.Customer{}, err
	}
	if err := s.recordAudit(ctx, "customer.create", "customer", created.ID, map[string]any{
		"name": created.Name, "currency": created.Currency,
	}); err != nil {
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
	if err := s.db.UpdateCustomer(ctx, c); err != nil {
		return err
	}
	return s.recordAudit(ctx, "customer.update", "customer", c.ID, map[string]any{"name": c.Name})
}

// SetCustomerArchived retires a customer from the pickers, or restores it.
func (s *Service) SetCustomerArchived(ctx context.Context, id int64, archived bool) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "customer", ID: id, CustomerID: id,
	}); err != nil {
		return notFoundFor(err)
	}
	if err := s.db.SetCustomerArchived(ctx, id, archived); err != nil {
		return err
	}
	action := "customer.restore"
	if archived {
		action = "customer.archive"
	}
	return s.recordAudit(ctx, action, "customer", id, nil)
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
	return s.db.ListCustomers(ctx, scope, includeArchived)
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
	return c, nil
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

	created, err := s.db.CreateProject(ctx, p)
	if err != nil {
		return domain.Project{}, err
	}
	if err := s.recordAudit(ctx, "project.create", "project", created.ID, map[string]any{
		"name": created.Name, "customer_id": created.CustomerID,
	}); err != nil {
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
	if err := s.db.UpdateProject(ctx, p); err != nil {
		return err
	}
	return s.recordAudit(ctx, "project.update", "project", p.ID, map[string]any{"name": p.Name})
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
	if err := s.db.SetProjectArchived(ctx, id, archived); err != nil {
		return err
	}
	action := "project.restore"
	if archived {
		action = "project.archive"
	}
	return s.recordAudit(ctx, action, "project", id, nil)
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
	return s.db.ListProjects(ctx, scope, customerID, includeArchived)
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
	return p, nil
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

	created, err := s.db.CreateAssignment(ctx, a)
	if err != nil {
		return domain.Assignment{}, err
	}
	if err := s.recordAudit(ctx, "assignment.create", "assignment", created.ID, map[string]any{
		"name": created.Name, "project_id": created.ProjectID,
	}); err != nil {
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
	if err := s.db.UpdateAssignment(ctx, a); err != nil {
		return err
	}
	return s.recordAudit(ctx, "assignment.update", "assignment", a.ID, map[string]any{"name": a.Name})
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
	if err := s.db.SetAssignmentArchived(ctx, id, archived); err != nil {
		return err
	}
	action := "assignment.restore"
	if archived {
		action = "assignment.archive"
	}
	return s.recordAudit(ctx, action, "assignment", id, nil)
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
	return s.db.RecentAssignments(ctx, actor.ID, limit)
}

// ------------------------------------------------------------------ shared --

// recordAudit writes an audit row for a change that has already been made
// through a store method.
//
// Catalogue changes are single statements, so unlike time entries they do not
// need the change and the audit row to share a transaction with other work.
// They do still need the record itself, so this is never optional: a failure
// here is returned to the caller rather than swallowed.
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
