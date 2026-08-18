package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Recording again what was recorded before: copying a day or a week, routines,
// and switching from one timer to the next.
//
// All three exist for the same reason. Most time is not novel - the same few
// assignments, the same stand-up, the same lunch - and a tool that makes a
// person retype it is a tool they will fill in on Friday from memory, which is
// where under-reporting comes from.
//
// All three are also deliberately explicit. Nothing here creates time without
// somebody asking for it on the day: see
// docs/adr/0027-routines-are-offered-not-fired.md.

// quickStartWindow is how far back "what do I usually work on" looks.
//
// Six weeks: long enough that a fortnight on one client does not erase
// everything else, short enough that finished work drops off the list without
// anybody having to archive it.
const quickStartWindow = 42 * 24 * time.Hour

// QuickStart returns the assignments to offer as one-click starts.
//
// Favourites first, then what has been used most in the recent window. Capped
// at a number somebody can scan without reading - a list of thirty one-click
// buttons is a list nobody clicks.
func (s *Service) QuickStart(ctx context.Context, limit int) ([]domain.Assignment, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 10 {
		limit = 10
	}
	return s.db.QuickStartAssignments(ctx, actor.ID, s.now().Add(-quickStartWindow), limit)
}

// ---------------------------------------------------------------- copying ---

// CopyResult reports what a copy did, and what it declined to do.
type CopyResult struct {
	// Created is how many entries were written.
	Created int
	// SkippedRunning counts timers that were running on the source day. A
	// running timer has no duration yet; copying one would either invent a
	// length or start a second timer nobody asked for.
	SkippedRunning int
	// SkippedArchived counts entries whose assignment has since been archived.
	SkippedArchived int
	// Target is the day or week start the entries landed on.
	Target time.Time
}

// Copied reports whether anything was written.
func (r CopyResult) Copied() bool { return r.Created > 0 }

// CopyDay duplicates one day's entries onto another day.
//
// Times of day are preserved, so a copied day looks like the day it came from.
// Amounts are not copied: each new entry is priced afresh, because the target
// day may fall in a different contract period
// (docs/adr/0026-dated-contract-terms.md).
func (s *Service) CopyDay(ctx context.Context, from, to time.Time) (CopyResult, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	loc := locationFor(actor)
	source := dayRange(from, loc)
	target := startOfDay(to, loc)

	entries, err := s.dayEntries(ctx, actor.ID, source)
	if err != nil {
		return CopyResult{}, err
	}
	if len(entries) == 0 {
		return CopyResult{}, fmt.Errorf(
			"%w: there is nothing recorded on %s to copy", ErrValidation,
			from.In(loc).Format("2006-01-02"))
	}

	return s.copyEntries(ctx, entries, source.start, target, loc)
}

// CopyWeek duplicates a whole week onto another week, day for day.
//
// Monday's entries land on the target week's Monday. Copying a week onto a week
// that already has time in it adds to it rather than replacing it: replacing
// would silently delete work, and there is no way to ask for that here.
func (s *Service) CopyWeek(ctx context.Context, from, to time.Time) (CopyResult, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	loc := locationFor(actor)

	sourceStart, err := domain.ParseWeekStart(
		domain.WeekStartFor(from, settings.WeekStart, loc), loc)
	if err != nil {
		return CopyResult{}, err
	}
	targetStart, err := domain.ParseWeekStart(
		domain.WeekStartFor(to, settings.WeekStart, loc), loc)
	if err != nil {
		return CopyResult{}, err
	}
	if sourceStart.Equal(targetStart) {
		return CopyResult{}, fmt.Errorf(
			"%w: that is the same week", ErrValidation)
	}

	entries, err := s.rangeEntries(ctx, actor.ID, sourceStart, domain.WeekEnd(sourceStart))
	if err != nil {
		return CopyResult{}, err
	}
	if len(entries) == 0 {
		return CopyResult{}, fmt.Errorf(
			"%w: there is nothing recorded in the week of %s to copy",
			ErrValidation, sourceStart.Format("2006-01-02"))
	}

	return s.copyEntries(ctx, entries, sourceStart, targetStart, loc)
}

// copyEntries writes each entry again, shifted by the gap between the source
// and the target.
//
// Shifting rather than re-basing each entry onto the target day keeps a week
// copy aligned: an entry on the source Wednesday lands on the target Wednesday,
// including its time of day.
func (s *Service) copyEntries(ctx context.Context, entries []domain.TimeEntry, sourceStart, targetStart time.Time, loc *time.Location) (CopyResult, error) {
	result := CopyResult{Target: targetStart}
	offsetDays := int(targetStart.Sub(sourceStart).Hours() / 24)

	for _, entry := range entries {
		if entry.Running() {
			// A running timer has no length yet. Copying it would mean either
			// inventing one or starting a second timer nobody asked for.
			result.SkippedRunning++
			continue
		}
		assignment, err := s.db.GetAssignment(ctx, entry.AssignmentID)
		if err != nil {
			return CopyResult{}, err
		}
		if assignment.Archived() {
			result.SkippedArchived++
			continue
		}

		started := entry.StartedAt.In(loc).AddDate(0, 0, offsetDays)
		// CreateEntry does the rest: the period lock, the authorisation, the
		// billing for the target day, the audit row. Copying deliberately goes
		// through the same door as typing, so a copy into a submitted week is
		// refused exactly as a manual entry would be.
		if _, err := s.CreateEntry(ctx, EntryInput{
			AssignmentID:    entry.AssignmentID,
			StartedAt:       started,
			DurationSeconds: entry.DurationSeconds,
			Note:            entry.Note,
			Billable:        entry.Billable,
			Kind:            entry.KindOrDefault(),
			Tags:            entry.Tags,
		}); err != nil {
			return CopyResult{}, err
		}
		result.Created++
	}
	return result, nil
}

// dayEntries and rangeEntries load one person's own entries over a period.
func (s *Service) dayEntries(ctx context.Context, userID int64, day dayBounds) ([]domain.TimeEntry, error) {
	return s.rangeEntries(ctx, userID, day.start, day.end)
}

func (s *Service) rangeEntries(ctx context.Context, userID int64, from, to time.Time) ([]domain.TimeEntry, error) {
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.db.ListEntries(ctx, store.EntryFilter{
		UserID: userID, From: from, To: to, Scope: scope,
	})
	if err != nil {
		return nil, err
	}
	// Only confirmed time: a proposal somebody has not accepted is not theirs
	// to copy, and a flagged entry is one they have already been asked to look
	// at rather than duplicate.
	var usable []domain.TimeEntry
	for _, entry := range entries {
		if entry.Counts() || entry.Running() {
			usable = append(usable, entry)
		}
	}
	sort.Slice(usable, func(i, j int) bool {
		return usable[i].StartedAt.Before(usable[j].StartedAt)
	})
	return usable, nil
}

// dayBounds is a half-open day.
type dayBounds struct{ start, end time.Time }

func dayRange(day time.Time, loc *time.Location) dayBounds {
	start := startOfDay(day, loc)
	return dayBounds{start: start, end: start.AddDate(0, 0, 1)}
}

func startOfDay(day time.Time, loc *time.Location) time.Time {
	local := day.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// --------------------------------------------------------------- routines ---

// DueRoutine is a routine that applies to a day, and whether it has been used.
type DueRoutine struct {
	Routine domain.Routine
	// AlreadyRecorded is true when an entry on the same assignment, of the same
	// length, already exists that day. It is a guess rather than a fact - there
	// is no link from an entry back to the routine that suggested it - but it
	// is what stops the day view offering somebody their lunch twice.
	AlreadyRecorded bool
}

// RoutinesDue returns the routines that apply to a day.
func (s *Service) RoutinesDue(ctx context.Context, day time.Time) ([]DueRoutine, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	routines, err := s.db.ListRoutines(ctx, actor.ID, true)
	if err != nil {
		return nil, err
	}
	if len(routines) == 0 {
		return nil, nil
	}

	loc := locationFor(actor)
	local := day.In(loc)
	existing, err := s.dayEntries(ctx, actor.ID, dayRange(day, loc))
	if err != nil {
		return nil, err
	}

	var due []DueRoutine
	for _, routine := range routines {
		if !routine.AppliesOn(local) {
			continue
		}
		item := DueRoutine{Routine: routine}
		for _, entry := range existing {
			if entry.AssignmentID == routine.AssignmentID &&
				entry.DurationSeconds == routine.DurationSeconds {
				item.AlreadyRecorded = true
				break
			}
		}
		due = append(due, item)
	}
	return due, nil
}

// ApplyRoutine records the entry a routine describes, on a day.
//
// This is the only thing that turns a routine into time, and a person has to
// ask for it. See ADR-0027 for why it is not a scheduler.
func (s *Service) ApplyRoutine(ctx context.Context, routineID int64, day time.Time) (domain.TimeEntry, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	routine, err := s.db.GetRoutine(ctx, routineID)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	// A routine belongs to the person who made it. It is a way of typing, not a
	// shared object, and applying somebody else's would record their habits as
	// your time.
	if routine.UserID != actor.ID {
		return domain.TimeEntry{}, notFoundFor(ErrForbidden)
	}

	loc := locationFor(actor)
	started := startOfDay(day, loc)
	if routine.StartTime != "" {
		parsed, parseErr := time.ParseInLocation("2006-01-02 15:04",
			started.Format("2006-01-02")+" "+routine.StartTime, loc)
		if parseErr != nil {
			return domain.TimeEntry{}, fmt.Errorf(
				"%w: %q is not a time of day", ErrValidation, routine.StartTime)
		}
		started = parsed
	} else {
		// No time of its own: the start of the working day, as for any other
		// entry that does not carry one.
		started = started.Add(9 * time.Hour)
	}

	return s.CreateEntry(ctx, EntryInput{
		AssignmentID:    routine.AssignmentID,
		StartedAt:       started,
		DurationSeconds: routine.DurationSeconds,
		Note:            routine.Note,
		Billable:        routine.Billable,
		Kind:            routine.Kind,
		Tags:            routine.Tags,
	})
}

// ApplyAllRoutines records every routine due on a day that is not already there.
func (s *Service) ApplyAllRoutines(ctx context.Context, day time.Time) (int, error) {
	due, err := s.RoutinesDue(ctx, day)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, item := range due {
		if item.AlreadyRecorded {
			continue
		}
		if _, err := s.ApplyRoutine(ctx, item.Routine.ID, day); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// Routines lists the acting user's templates.
func (s *Service) Routines(ctx context.Context, activeOnly bool) ([]domain.Routine, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	return s.db.ListRoutines(ctx, actor.ID, activeOnly)
}

// SaveRoutine creates or updates one.
func (s *Service) SaveRoutine(ctx context.Context, routine domain.Routine) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	routine.UserID = actor.ID
	if err := routine.Validate(); err != nil {
		return err
	}
	// The assignment has to be one this person may record against, or a routine
	// would be a way around the authoriser.
	assignment, err := s.db.GetAssignment(ctx, routine.AssignmentID)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionCreate, auth.Resource{
		Type: "time_entry", OwnerID: actor.ID,
		ProjectID: assignment.ProjectID, CustomerID: assignment.CustomerID,
	}); err != nil {
		return err
	}

	if routine.ID != 0 {
		existing, err := s.db.GetRoutine(ctx, routine.ID)
		if err != nil {
			return err
		}
		if existing.UserID != actor.ID {
			return notFoundFor(ErrForbidden)
		}
		return s.db.UpdateRoutine(ctx, routine)
	}
	_, err = s.db.CreateRoutine(ctx, routine)
	return err
}

// DeleteRoutine removes a template. Entries it produced are ordinary entries and
// stay exactly as they are.
func (s *Service) DeleteRoutine(ctx context.Context, id int64) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	existing, err := s.db.GetRoutine(ctx, id)
	if err != nil {
		return err
	}
	if existing.UserID != actor.ID {
		return notFoundFor(ErrForbidden)
	}
	return s.db.DeleteRoutine(ctx, id)
}

// ------------------------------------------------------------- switching ----

// SwitchResult reports what a switch did.
type SwitchResult struct {
	Stopped []domain.TimeEntry
	Started domain.TimeEntry
}

// SwitchTo stops what is running and starts something else, in one action.
//
// Concurrent timers are allowed and are the right model for genuinely parallel
// work (docs/adr/0004-concurrent-timers.md). But the commonest thing that
// happens to a running timer is not "and now also" - it is "and now instead",
// and doing that as stop-then-start leaves a gap if the person is interrupted
// between the two clicks, and leaves both running if they forget the first.
//
// So this is an explicit alternative rather than a replacement: Start still
// starts alongside, and Switch is the one-click version of the common case.
func (s *Service) SwitchTo(ctx context.Context, assignmentID int64, note string) (SwitchResult, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return SwitchResult{}, err
	}

	running, err := s.db.ListRunningEntries(ctx, actor.ID)
	if err != nil {
		return SwitchResult{}, err
	}

	// Stopping first, and stopping everything: a switch that left one of three
	// timers going would be a worse answer than either alternative. Each stop
	// goes through StopTimer, so each is billed, flagged and audited normally.
	var result SwitchResult
	for _, entry := range running {
		if entry.AssignmentID == assignmentID {
			// Already on it. Stopping and restarting would split one stretch of
			// work into two entries for no reason.
			continue
		}
		stopped, stopErr := s.StopTimer(ctx, entry.ID)
		if stopErr != nil {
			return SwitchResult{}, stopErr
		}
		result.Stopped = append(result.Stopped, stopped)
	}

	for _, entry := range running {
		if entry.AssignmentID == assignmentID {
			// The requested timer was already running; the switch stopped the
			// others and this is the answer.
			result.Started = entry
			return result, nil
		}
	}

	if result.Started, err = s.StartTimer(ctx, assignmentID, note); err != nil {
		return SwitchResult{}, err
	}
	return result, nil
}
