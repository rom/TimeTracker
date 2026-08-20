package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Idle observations: recording what the page saw, and applying what the person
// decided about it (ADR-0033).
//
// Every method here is scoped to the acting user's own time. An observation is
// evidence about somebody's day that reduces their recorded hours if they accept
// it, and nobody else gets to file one or answer one - not an administrator
// either. That is stricter than the authorisation model needs to be and is
// deliberate: "your tracker decided you were away" is a sentence this
// application should never be able to produce.

// IdleReport is an observation with the consequences of each choice worked out,
// so the interface can put the numbers on the buttons.
type IdleReport struct {
	Observation domain.IdleObservation
	Entry       domain.TimeEntry
	// Discard and Split are what each decision would do, and CanDiscard and
	// CanSplit say whether each is on offer at all. Split is absent when
	// nothing followed the stretch, because then it is a discard; both are
	// absent when the stretch covers the whole entry, which leaves nothing to
	// keep either side of it and makes deleting the entry the only sensible
	// answer - one the person already has, and not one to be reached by
	// pressing a button labelled "discard".
	Discard    domain.IdleOutcome
	Split      domain.IdleOutcome
	CanDiscard bool
	CanSplit   bool
}

// RecordIdle stores a stretch the page saw nothing during.
//
// The times come from the browser, so they are clamped to the entry and to the
// present before anything is written, and a stretch under the configured
// threshold is dropped rather than stored. Reporting the same absence twice -
// which happens whenever a machine is woken and slept again inside one lunch
// hour - widens the existing row instead of asking twice about one hour.
//
// It returns whether anything was recorded. A page that reports an unusable
// stretch has not done anything wrong; it has simply been told nothing came of
// it.
func (s *Service) RecordIdle(ctx context.Context, entryID int64, from, to time.Time, source domain.IdleSource) (bool, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return false, err
	}
	if !domain.KnownIdleSource(source) {
		return false, fmt.Errorf("%w: unknown idle source %q", ErrValidation, source)
	}

	entry, err := s.db.GetEntry(ctx, entryID)
	if err != nil {
		return false, err
	}
	if entry.UserID != actor.ID {
		// Not ErrForbidden: an observation about somebody else's entry is not a
		// permission this application has, so the entry may as well not exist.
		return false, ErrNotFound
	}

	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return false, err
	}
	if settings.IdleSeconds <= 0 {
		// Idle observation is switched off for the instance. A page that has not
		// noticed yet is answered politely rather than with an error.
		return false, nil
	}

	from, to, ok := domain.ClampIdle(entry, from, to, s.now(), settings.IdleSeconds)
	if !ok {
		return false, nil
	}

	existing, err := s.db.OverlappingIdleObservation(ctx, entryID, from, to)
	switch {
	case err == nil:
		if !from.Before(existing.StartedAt) && !to.After(existing.EndedAt) {
			// Already covered. Nothing to widen and nothing to say.
			return false, nil
		}
		return true, s.db.WidenIdleObservation(ctx, existing.ID, from, to)
	case errors.Is(err, store.ErrNotFound):
		// The ordinary case: a stretch nobody has reported yet.
	default:
		return false, err
	}

	if _, err := s.db.CreateIdleObservation(ctx, domain.IdleObservation{
		EntryID: entryID, UserID: actor.ID,
		StartedAt: from, EndedAt: to, Source: source,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// PendingIdle returns the observations awaiting a decision, with the effect of
// each decision worked out.
//
// Only stopped entries: see UnresolvedIdleObservations. An observation whose
// entry has since been edited into a shape the stretch no longer fits is
// skipped rather than offered, because the arithmetic behind the buttons would
// be about an entry that no longer exists.
func (s *Service) PendingIdle(ctx context.Context) ([]IdleReport, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}

	observations, err := s.db.UnresolvedIdleObservations(ctx, actor.ID, 20)
	if err != nil {
		return nil, err
	}

	reports := make([]IdleReport, 0, len(observations))
	for _, o := range observations {
		entry, err := s.db.GetEntry(ctx, o.EntryID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		report, ok := idleReportFor(entry, o)
		if !ok {
			continue
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// RunningIdle returns unresolved observations on timers that are still going.
//
// These are shown as a notice rather than as a question: the interval is still
// being measured, so there is nothing stable to rewrite yet. Saying so while the
// timer runs is the point - it is the moment somebody can still remember what
// they were doing at half past twelve.
func (s *Service) RunningIdle(ctx context.Context) ([]domain.IdleObservation, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	return s.db.RunningIdleObservations(ctx, actor.ID)
}

// ResolveIdle applies a decision.
//
// Keep records the decision and changes nothing. Discard ends the entry where
// the stretch began. Split does that and records a second entry for what
// followed. The arithmetic is the domain's, so the sentence the interface put on
// the button and the change this makes come from one function.
func (s *Service) ResolveIdle(ctx context.Context, id int64, resolution domain.IdleResolution) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	if !domain.KnownIdleResolution(resolution) {
		return fmt.Errorf("%w: unknown resolution %q", ErrValidation, resolution)
	}

	observation, err := s.db.GetIdleObservation(ctx, id)
	if err != nil {
		return err
	}
	if observation.UserID != actor.ID {
		return ErrNotFound
	}
	if observation.Resolved() {
		return fmt.Errorf("%w: that has already been decided", ErrConflict)
	}

	entry, err := s.db.GetEntry(ctx, observation.EntryID)
	if err != nil {
		return err
	}
	if err := s.canModify(ctx, entry); err != nil {
		return notFoundFor(err)
	}
	// A submitted or approved week is closed to this as much as to any other
	// edit. Removing an hour from an approved week by answering a prompt would
	// be the same change as removing it by hand, and the lock exists precisely
	// so that cannot happen quietly.
	if err := s.checkPeriodOpen(ctx, entry.UserID, entry.StartedAt); err != nil {
		return err
	}

	outcome, err := domain.ResolveIdle(entry, observation, resolution)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err)
	}

	trimmed := entry
	second := entry
	// The id of the entry a split creates, and zero for every other decision -
	// so the audit trail says "this made a second entry" only when it did.
	var splitID int64
	if resolution != domain.IdleKeep {
		trimmed.StartedAt = outcome.KeepFrom
		end := outcome.KeepTo
		trimmed.EndedAt = &end
		trimmed.DurationSeconds = domain.SecondsBetween(outcome.KeepFrom, outcome.KeepTo)
		// A person has now looked at this entry, which is what a review flag
		// asks for.
		trimmed.Flagged = false
		if err := s.applyBillingTo(ctx, &trimmed); err != nil {
			return err
		}

		if outcome.Splits {
			second.ID = 0
			second.StartedAt = outcome.SplitFrom
			secondEnd := outcome.SplitTo
			second.EndedAt = &secondEnd
			second.DurationSeconds = domain.SecondsBetween(outcome.SplitFrom, outcome.SplitTo)
			second.Flagged = false
			if err := s.applyBillingTo(ctx, &second); err != nil {
				return err
			}
		}
	}

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if txErr := store.ResolveIdleObservationTx(ctx, tx, id, resolution, s.now()); txErr != nil {
			return txErr
		}
		if resolution != domain.IdleKeep {
			if txErr := updateEntryTx(ctx, tx, trimmed); txErr != nil {
				return txErr
			}
			if outcome.Splits {
				created, txErr := createEntryTx(ctx, tx, second)
				if txErr != nil {
					return txErr
				}
				splitID = created.ID
			}
			// Any other question about this entry was asked about the interval
			// it used to have. Answering those against the new one would be
			// arithmetic on an entry that no longer exists, so they are closed
			// with the decision that caused it.
			if txErr := store.ResolveIdleObservationsForEntryTx(ctx, tx, entry.ID,
				resolution, s.now()); txErr != nil {
				return txErr
			}
		}
		return s.audit(ctx, tx, "idle.resolve", "time_entry", entry.ID, 0, map[string]any{
			"observation_id":  id,
			"source":          string(observation.Source),
			"resolution":      string(resolution),
			"observed_from":   observation.StartedAt,
			"observed_to":     observation.EndedAt,
			"removed_seconds": outcome.RemovedSeconds,
			"kept_seconds":    outcome.KeptSeconds,
			"split_entry_id":  splitID,
		})
	})
	if err != nil {
		return err
	}

	s.auditLog(ctx, "idle.resolve", "time_entry", entry.ID)
	return nil
}

// IdleThresholdSeconds is how long a stretch has to be before the page reports
// it, or zero when idle observation is off.
//
// The page needs the number to do the watching, so it is rendered into the
// document rather than fetched.
func (s *Service) IdleThresholdSeconds(ctx context.Context) (int64, error) {
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return 0, err
	}
	return settings.IdleSeconds, nil
}

// idleReportFor works out what each decision would do to one entry.
//
// It returns false only when the observation no longer belongs to the entry at
// all - the entry was edited out from under it, or is running again. A stretch
// that still fits is always reported, even when the only answer left is to keep
// it: an observation nobody is shown is an observation that may as well not have
// been made.
func idleReportFor(entry domain.TimeEntry, o domain.IdleObservation) (IdleReport, bool) {
	if entry.Running() {
		return IdleReport{}, false
	}
	if !o.EndedAt.After(entry.StartedAt) || !entry.EndedAt.After(o.StartedAt) {
		return IdleReport{}, false
	}

	report := IdleReport{Observation: o, Entry: entry}
	if discard, err := domain.ResolveIdle(entry, o, domain.IdleDiscard); err == nil {
		report.Discard, report.CanDiscard = discard, true
	}
	if split, err := domain.ResolveIdle(entry, o, domain.IdleSplit); err == nil && split.Splits {
		report.Split, report.CanSplit = split, true
	}
	return report, true
}
