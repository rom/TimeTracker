package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Recording what has been billed.
//
// The money layer could say what an hour was worth and could not say whether
// anybody had been charged for it. So it could not answer the question the
// whole layer exists to serve - what is still unbilled - and nothing stopped
// the same fortnight going out twice.
//
// A run is this application's record that an invoice went out: which customer,
// which days, what it came to. It is not an invoice and does not try to be;
// invoicing is out of scope (DESIGN.md §11) and the reference field is where
// the number from the system that does issue them goes.
//
// Who may do it: whoever administers the customer. That is an administrator,
// or a manager of a project of that customer - the same authority that sets
// the customer's rates, which is the right shape, since billing is the act
// those rates exist for. A member cannot, and neither can a client.

// BillingCandidates is what a run would contain if it were made now.
type BillingCandidates struct {
	CustomerID   int64
	CustomerName string
	StartsOn     string
	EndsOn       string
	// Entries are the unbilled, billable, counting entries in the range,
	// newest first. They are the detail behind the totals, so somebody can see
	// what they are about to invoice rather than a number.
	Entries []domain.TimeEntry
	// BillableSeconds and AmountMinor are what they come to. The seconds are
	// the billable ones - what rounding produced - rather than the recorded
	// ones, because that is what the amount was computed from.
	BillableSeconds int64
	AmountMinor     int64
	// Currency is what they are in, and Currencies the number of distinct ones
	// found. More than one is refused: an invoice is in a currency.
	Currency   string
	Currencies int
}

// BillingInput is a request to record a run.
type BillingInput struct {
	CustomerID int64
	StartsOn   string
	EndsOn     string
	Reference  string
	Note       string
}

// BillingCandidatesFor reports what is still to invoice for one customer.
func (s *Service) BillingCandidatesFor(ctx context.Context, customerID int64, from, to time.Time) (BillingCandidates, error) {
	if err := s.mayBill(ctx, customerID); err != nil {
		return BillingCandidates{}, err
	}

	customer, err := s.db.GetCustomer(ctx, customerID)
	if err != nil {
		return BillingCandidates{}, err
	}

	// The actor's own scope, as every other listing uses. A zero Scope means
	// "nothing" by design - the fail-closed reading for somebody who belongs
	// to no projects - so leaving it out is not "no restriction", it is an
	// empty invoice. That was this function's first bug.
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return BillingCandidates{}, err
	}

	// To is exclusive in the store and inclusive on the form: somebody billing
	// March means the 31st included.
	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		CustomerID: customerID, From: from, To: to.AddDate(0, 0, 1),
		CountingOnly: true, BillableOnly: true, UnbilledOnly: true,
		Scope: scope,
	})
	if err != nil {
		return BillingCandidates{}, err
	}

	const iso = "2006-01-02"
	found := BillingCandidates{
		CustomerID: customerID, CustomerName: customer.Name,
		StartsOn: from.Format(iso), EndsOn: to.Format(iso),
		Entries: s.narrowEntries(ctx, entries),
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		// The filter above asked for counting entries only, and this asks
		// again in Go. Not redundant: a proposal nobody has accepted, or an
		// hour flagged for review, reaching an invoice is the failure ASR-008
		// exists to prevent, and the two conditions being expressed in two
		// languages is how a change to one gets noticed.
		if !entry.Counts() {
			continue
		}
		found.BillableSeconds += entry.BillableSeconds
		found.AmountMinor += entry.AmountMinor
		if entry.Currency != "" && !seen[entry.Currency] {
			seen[entry.Currency] = true
			found.Currencies++
			found.Currency = entry.Currency
		}
	}
	if found.Currencies > 1 {
		// Named rather than summed. Adding kronor to euros produces a number
		// that is wrong in a way nobody notices until the invoice is queried.
		found.Currency = ""
	}
	return found, nil
}

// RecordBillingRun writes the run and stamps its entries, in one transaction.
func (s *Service) RecordBillingRun(ctx context.Context, in BillingInput) (domain.BillingRun, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.BillingRun{}, err
	}
	if err := s.mayBill(ctx, in.CustomerID); err != nil {
		return domain.BillingRun{}, err
	}

	const iso = "2006-01-02"
	from, err := time.Parse(iso, in.StartsOn)
	if err != nil {
		return domain.BillingRun{}, fmt.Errorf("%w: the first day is not a date", ErrValidation)
	}
	to, err := time.Parse(iso, in.EndsOn)
	if err != nil {
		return domain.BillingRun{}, fmt.Errorf("%w: the last day is not a date", ErrValidation)
	}
	if to.Before(from) {
		return domain.BillingRun{}, fmt.Errorf("%w: the range ends before it starts", ErrValidation)
	}

	candidates, err := s.BillingCandidatesFor(ctx, in.CustomerID, from, to)
	if err != nil {
		return domain.BillingRun{}, err
	}
	if len(candidates.Entries) == 0 {
		// An empty run is a record of nothing, and would make "unbilled" read
		// as "already invoiced" for a period nobody invoiced.
		return domain.BillingRun{}, fmt.Errorf(
			"%w: there is no unbilled time for %s in that range",
			ErrValidation, candidates.CustomerName)
	}
	if candidates.Currencies > 1 {
		return domain.BillingRun{}, fmt.Errorf(
			"%w: that range holds more than one currency, which is more than "+
				"one invoice; narrow it or bill each separately", ErrValidation)
	}

	ids := make([]int64, 0, len(candidates.Entries))
	for _, entry := range candidates.Entries {
		if !entry.Counts() {
			continue
		}
		ids = append(ids, entry.ID)
	}

	run := domain.BillingRun{
		CustomerID: in.CustomerID, StartsOn: in.StartsOn, EndsOn: in.EndsOn,
		Reference: in.Reference, Note: in.Note,
		Currency: candidates.Currency, EntryCount: len(ids),
		BillableSeconds: candidates.BillableSeconds,
		AmountMinor:     candidates.AmountMinor,
		CreatedBy:       actor.ID, CreatedAt: s.now(),
	}

	var created domain.BillingRun
	err = s.mutate(ctx, "billing_run.create", "billing_run", map[string]any{
		"customer_id": in.CustomerID, "from": in.StartsOn, "to": in.EndsOn,
		"entries": len(ids), "amount_minor": run.AmountMinor,
		"currency": run.Currency, "reference": in.Reference,
	}, func(tx *sql.Tx) (int64, error) {
		written, txErr := store.CreateBillingRunTx(ctx, tx, run)
		if txErr != nil {
			return 0, txErr
		}
		created = written

		stamped, txErr := store.StampEntriesTx(ctx, tx, written.ID, ids)
		if txErr != nil {
			return 0, txErr
		}
		// Every candidate, or none of it. A row that did not update was
		// claimed by another run between reading the candidates and writing
		// this one, and an invoice that quietly covers fewer hours than it
		// says is worse than one that is not written.
		if stamped != int64(len(ids)) {
			return 0, fmt.Errorf("%w: some of that time was billed by somebody "+
				"else while this was being prepared; look again", ErrValidation)
		}
		return written.ID, nil
	})
	if err != nil {
		return domain.BillingRun{}, err
	}
	return created, nil
}

// VoidBillingRun cancels a run and releases its entries.
//
// Cancelled rather than deleted: an invoice that went out and was credited is
// part of the account's history, and the next question after "was this billed"
// is "what happened to it". The entries become unbilled again and go on the
// next run.
func (s *Service) VoidBillingRun(ctx context.Context, id int64) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	run, err := s.db.GetBillingRun(ctx, id)
	if err != nil {
		return err
	}
	if err := s.mayBill(ctx, run.CustomerID); err != nil {
		return err
	}
	if run.Voided() {
		return fmt.Errorf("%w: that run was already voided", ErrValidation)
	}

	return s.mutate(ctx, "billing_run.void", "billing_run", map[string]any{
		"customer_id": run.CustomerID, "reference": run.Reference,
		"entries": run.EntryCount, "amount_minor": run.AmountMinor,
	}, func(tx *sql.Tx) (int64, error) {
		return id, store.VoidBillingRunTx(ctx, tx, id, actor.ID, formatRunTime(s.now()))
	})
}

// BillingRuns lists what has been billed, newest first.
func (s *Service) BillingRuns(ctx context.Context, customerID int64, limit int) ([]domain.BillingRun, error) {
	// Reading the billing history is reading money, so it is bounded by the
	// same authority that may create it. A listing names no single customer
	// when none is asked for, which the authoriser answers by role.
	if err := s.mayBill(ctx, customerID); err != nil {
		return nil, err
	}
	return s.db.ListBillingRuns(ctx, customerID, limit)
}

// BillingRunSummary loads one without its entries, for a screen that only
// needs to name it - the confirmation before voiding.
func (s *Service) BillingRunSummary(ctx context.Context, id int64) (domain.BillingRun, error) {
	run, err := s.db.GetBillingRun(ctx, id)
	if err != nil {
		return domain.BillingRun{}, err
	}
	if err := s.mayBill(ctx, run.CustomerID); err != nil {
		return domain.BillingRun{}, err
	}
	return run, nil
}

// BillingRun loads one, with the entries it covers.
func (s *Service) BillingRun(ctx context.Context, id int64) (domain.BillingRun, []domain.TimeEntry, error) {
	run, err := s.db.GetBillingRun(ctx, id)
	if err != nil {
		return domain.BillingRun{}, nil, err
	}
	if err := s.mayBill(ctx, run.CustomerID); err != nil {
		return domain.BillingRun{}, nil, err
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return domain.BillingRun{}, nil, err
	}
	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		BillingRunID: id, Scope: scope,
	})
	if err != nil {
		return domain.BillingRun{}, nil, err
	}
	// Narrowed like every other read of entries. A client cannot reach this -
	// mayBill refuses them above - so this is belt and braces rather than the
	// enforcement; the enforcement being in one place is exactly why the
	// architecture test insists every path calls it.
	return run, s.narrowEntries(ctx, entries), nil
}

// checkNotBilled refuses a change to time that has already been invoiced.
//
// The same shape as the week lock beside it, and for a sharper reason: an
// approved week that changes is a timesheet somebody has to re-approve, while
// a billed hour that changes is an invoice that no longer matches what was
// sent, and nothing anywhere would say so. Voiding the run is the way back,
// which the message says - a refusal that does not name its remedy is a dead
// end.
func (s *Service) checkNotBilled(ctx context.Context, entry domain.TimeEntry) error {
	if entry.BillingRunID == 0 {
		return nil
	}
	run, err := s.db.GetBillingRun(ctx, entry.BillingRunID)
	if err != nil {
		// A run that cannot be read is not a reason to allow the change.
		return err
	}
	where := run.Reference
	if where == "" {
		where = fmt.Sprintf("%s – %s", run.StartsOn, run.EndsOn)
	}
	return fmt.Errorf("%w: this time has been billed (%s). Void that billing "+
		"run first if it really needs to change", ErrValidation, where)
}

// mayBill decides whether the acting user may bill this customer.
func (s *Service) mayBill(ctx context.Context, customerID int64) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "customer", CustomerID: customerID, ID: customerID,
	}); err != nil {
		return notFoundFor(err)
	}
	return nil
}

// formatRunTime writes the instant a run was voided, in the same format the
// rest of the schema stores.
func formatRunTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
