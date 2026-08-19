package service

import (
	"context"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Dated contract terms, per customer and per project.
//
// See docs/adr/0026-dated-contract-terms.md. The service's job here is
// authorisation, validation that spans records, and presenting the history in
// a form a screen can render without re-deriving the resolution rules.

// TermsView is one scope's whole history of terms.
type TermsView struct {
	Scope   domain.TermsScope
	ScopeID int64
	// ScopeName is the customer's or project's name, for the heading.
	ScopeName string
	// CustomerName and CustomerID are filled for a project, so its screen can
	// link to the account's terms - which is where most of them will be.
	CustomerName string
	CustomerID   int64
	Currency     string

	// Terms are newest effective date first, which is the order they are
	// resolved in and the order somebody reads them in.
	Terms []domain.ContractTerms
	// Effective is what actually applies today, after the project's terms are
	// merged over the customer's. Shown because the merge is the part people
	// get wrong, and a screen that lists two revisions without saying what
	// comes out of them is a puzzle.
	Effective domain.RateRules
	// Inherited is what the customer's terms alone say today, so a project
	// screen can show what it is overriding. Empty for a customer.
	Inherited domain.RateRules
}

// ContractTerms returns one scope's dated terms and what they resolve to today.
func (s *Service) ContractTerms(ctx context.Context, scope domain.TermsScope, id int64) (TermsView, error) {
	if !scope.Valid() {
		return TermsView{}, fmt.Errorf("%w: unknown contract scope %q", ErrValidation, scope)
	}
	view := TermsView{Scope: scope, ScopeID: id}

	// Reading terms is reading commercial detail, so it takes the same
	// permission managing the catalogue does.
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "customer"}); err != nil {
		return TermsView{}, notFoundFor(err)
	}

	switch scope {
	case domain.TermsForCustomer:
		customer, err := s.db.GetCustomer(ctx, id)
		if err != nil {
			return TermsView{}, err
		}
		view.ScopeName = customer.Name
		view.CustomerID = customer.ID
		view.Currency = customer.Currency

	case domain.TermsForProject:
		project, err := s.db.GetProject(ctx, id)
		if err != nil {
			return TermsView{}, err
		}
		customer, err := s.db.GetCustomer(ctx, project.CustomerID)
		if err != nil {
			return TermsView{}, err
		}
		view.ScopeName = customer.Name + " / " + project.Name
		view.CustomerID = customer.ID
		view.CustomerName = customer.Name
		view.Currency = customer.Currency
	}

	terms, err := s.db.ListContractTerms(ctx, scope, id)
	if err != nil {
		return TermsView{}, err
	}
	view.Terms = terms

	today := s.now().Format("2006-01-02")
	if scope == domain.TermsForProject {
		if view.Inherited, err = s.db.ResolveTerms(ctx, view.CustomerID, 0, today); err != nil {
			return TermsView{}, err
		}
		if view.Effective, err = s.db.ResolveTerms(ctx, view.CustomerID, id, today); err != nil {
			return TermsView{}, err
		}
	} else {
		if view.Effective, err = s.db.ResolveTerms(ctx, id, 0, today); err != nil {
			return TermsView{}, err
		}
	}
	return view, nil
}

// TermsCurrency returns the currency a scope's amounts are typed in.
func (s *Service) TermsCurrency(ctx context.Context, scope domain.TermsScope, id int64) (string, error) {
	customerID := id
	if scope == domain.TermsForProject {
		project, err := s.db.GetProject(ctx, id)
		if err != nil {
			return "", err
		}
		customerID = project.CustomerID
	}
	customer, err := s.db.GetCustomer(ctx, customerID)
	if err != nil {
		return "", err
	}
	if customer.Currency != "" {
		return customer.Currency, nil
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings.DefaultCurrency, nil
}

// SaveContractTerms adds a revision, or updates one.
func (s *Service) SaveContractTerms(ctx context.Context, terms domain.ContractTerms) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "customer"}); err != nil {
		return notFoundFor(err)
	}
	if err := terms.Validate(); err != nil {
		return err
	}

	// A second revision starting on the same day as an existing one is refused
	// by name rather than by a unique-constraint error, because the fix is
	// obvious once it is stated and opaque otherwise.
	existing, err := s.db.ListContractTerms(ctx, terms.Scope, terms.ScopeID)
	if err != nil {
		return err
	}
	for _, other := range existing {
		if other.EffectiveFrom == terms.EffectiveFrom && other.ID != terms.ID {
			from := other.EffectiveFrom
			if from == "" {
				from = "the beginning"
			}
			return fmt.Errorf("%w: terms already start on %s; edit those instead",
				ErrConflict, from)
		}
	}

	action := "contract_terms.create"
	if terms.ID != 0 {
		action = "contract_terms.update"
		if err := s.db.UpdateContractTerms(ctx, terms); err != nil {
			return err
		}
	} else {
		created, createErr := s.db.CreateContractTerms(ctx, terms)
		if createErr != nil {
			return createErr
		}
		terms.ID = created.ID
	}

	// Terms decide what future work is worth, so a change to them is as
	// audit-worthy as a change to a rate.
	return s.recordAudit(ctx, action, "contract_terms", terms.ID, map[string]any{
		"scope":          string(terms.Scope),
		"scope_id":       terms.ScopeID,
		"effective_from": terms.EffectiveFrom,
		"note":           terms.Note,
	})
}

// DeleteContractTerms removes one revision.
//
// Safe to delete because nothing points at it: an entry carries the rate it was
// billed at, frozen, rather than a reference to the terms that produced it. What
// removing a revision changes is what *future* entries in that period are worth.
func (s *Service) DeleteContractTerms(ctx context.Context, id int64) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "customer"}); err != nil {
		return notFoundFor(err)
	}
	existing, err := s.db.GetContractTerms(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db.DeleteContractTerms(ctx, id); err != nil {
		return err
	}
	return s.recordAudit(ctx, "contract_terms.delete", "contract_terms", id, map[string]any{
		"scope":          string(existing.Scope),
		"scope_id":       existing.ScopeID,
		"effective_from": existing.EffectiveFrom,
	})
}

// TermsOn returns the rules in force for a project on a day, for callers that
// need to explain a figure rather than compute one.
func (s *Service) TermsOn(ctx context.Context, customerID, projectID int64, day time.Time) (domain.RateRules, error) {
	return s.db.ResolveTerms(ctx, customerID, projectID, day.Format("2006-01-02"))
}
