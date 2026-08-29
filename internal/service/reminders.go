package service

import (
	"context"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// End-of-day and end-of-week nudges (ADR-0034).
//
// Every one of these is a question asked of the timesheet as it stands, at the
// moment a screen renders. Nothing is queued and nothing is delivered, so a
// reminder cannot arrive about something already dealt with: recording the time
// makes the nudge untrue, and untrue nudges are not computed.
//
// The one thing that is stored is a dismissal, because "I know" is the only part
// of this that the timesheet cannot answer on its own.

// Reminder is re-exported so the HTTP layer can name it without reaching into
// the domain for a type it only renders.
type Reminder = domain.Reminder

// Reminders returns the nudges that are true for the acting user right now.
//
// Cheap by construction: it asks for counts rather than for rows, and the
// end-of-week half is not asked at all until the week is ending. It is called
// from the two screens somebody would act on - the day and the week - and its
// cost lands on the day view's budget, which is why none of it builds an entry.
func (s *Service) Reminders(ctx context.Context) ([]Reminder, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.RemindersEnabled {
		return nil, nil
	}

	// The person's own local day, not the server's. An instance serving two time
	// zones must nudge each of them at the end of their own afternoon.
	loc := locationFor(actor)
	now := s.now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	weekStart := startOfWeek(today, settings.WeekStart, loc)

	var candidates []Reminder

	if domain.PastReminderHour(now, settings.ReminderHour) {
		daily, err := s.dailyReminders(ctx, actor, today)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, daily...)
	}

	if domain.WeekIsEnding(now, weekStart, settings.ReminderHour) {
		weekly, err := s.weeklyReminder(ctx, actor, weekStart, loc)
		if err != nil {
			return nil, err
		}
		if weekly != nil {
			candidates = append(candidates, *weekly)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// One query for every scope in play rather than one per nudge.
	scopes := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, r := range candidates {
		if !seen[r.Scope] {
			seen[r.Scope] = true
			scopes = append(scopes, r.Scope)
		}
	}
	dismissed, err := s.db.DismissedReminders(ctx, actor.ID, scopes)
	if err != nil {
		return nil, err
	}

	out := make([]Reminder, 0, len(candidates))
	for _, r := range candidates {
		if dismissed[store.DismissalKey(r.Kind, r.Scope)] {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// dailyReminders are the three about today.
func (s *Service) dailyReminders(ctx context.Context, actor domain.User, today time.Time) ([]Reminder, error) {
	scope := domain.DayScope(today)
	var out []Reminder

	running, err := s.db.ListRunningEntries(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	if len(running) > 0 {
		out = append(out, Reminder{
			Kind: domain.ReminderRunningTimers, Scope: scope, Count: len(running),
		})
	}

	// Counted rather than listed: the nudge needs to know whether the day is
	// empty, not what is in it.
	recorded, err := s.CountEntries(ctx, EntryFilter{
		UserID: actor.ID,
		From:   today,
		To:     today.AddDate(0, 0, 1),
	})
	if err != nil {
		return nil, err
	}
	if recorded == 0 {
		out = append(out, Reminder{Kind: domain.ReminderEmptyDay, Scope: scope})
	}

	// A proposal nobody decides about is unbilled work, and the badge that says
	// so is easy to stop seeing.
	pending, err := s.PendingCount(ctx)
	if err != nil {
		return nil, err
	}
	if pending > 0 {
		out = append(out, Reminder{
			Kind: domain.ReminderPendingProposals, Scope: scope, Count: pending,
		})
	}
	return out, nil
}

// weeklyReminder is the one about the week, or nil when there is nothing to say.
//
// An empty week is deliberately not nudged about. Somebody who recorded nothing
// all week was on holiday, ill, or between clients, and telling them their empty
// week is unsubmitted is the application talking about itself rather than about
// their work.
func (s *Service) weeklyReminder(ctx context.Context, actor domain.User, weekStart time.Time, loc *time.Location) (*Reminder, error) {
	seconds, err := s.db.WeekSeconds(ctx, actor.ID, weekStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	if seconds == 0 {
		return nil, nil
	}

	period, err := s.db.GetPeriod(ctx, actor.ID, weekStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	if period.Status != domain.PeriodOpen && period.Status != domain.PeriodRejected {
		// Submitted or approved: there is nothing to remind anybody of. A
		// rejected week is open again and is exactly the case worth nudging.
		return nil, nil
	}

	// Which weekdays of the week have nothing on them, as an aside on the one
	// nudge rather than as five nudges of their own.
	empty, err := s.emptyWeekdays(ctx, actor, weekStart, loc)
	if err != nil {
		return nil, err
	}

	return &Reminder{
		Kind:   domain.ReminderUnsubmittedWeek,
		Scope:  domain.DayScope(weekStart),
		Days:   empty,
		Weekly: true,
	}, nil
}

// emptyWeekdays lists the Monday-to-Friday days of the week with no time on
// them, ignoring days still in the future.
//
// Weekdays only, because a weekend with nothing on it is not a gap in anybody's
// timesheet, and a nudge that says so every Monday is one people stop reading.
func (s *Service) emptyWeekdays(ctx context.Context, actor domain.User, weekStart time.Time, loc *time.Location) ([]time.Time, error) {
	now := s.now().In(loc)
	var empty []time.Time

	for offset := range 7 {
		day := weekStart.AddDate(0, 0, offset)
		if day.After(now) {
			break
		}
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		count, err := s.CountEntries(ctx, EntryFilter{
			UserID: actor.ID,
			From:   day,
			To:     day.AddDate(0, 0, 1),
		})
		if err != nil {
			return nil, err
		}
		if count == 0 {
			empty = append(empty, day)
		}
	}
	return empty, nil
}

// DismissReminder records that the acting user does not want to be told again
// about this one, in this scope.
//
// Not audited. A dismissal changes no time, no money and nothing anybody else
// can see; putting it in the audit trail would bury the entries that matter
// under a person's daily housekeeping.
func (s *Service) DismissReminder(ctx context.Context, kind domain.ReminderKind, scope string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	if !domain.KnownReminderKind(kind) {
		return ErrValidation
	}
	if _, err := time.Parse("2006-01-02", scope); err != nil {
		// The scope is a date and is written into a row that is read back
		// forever. Anything else would be a row nothing ever matches again.
		return ErrValidation
	}
	return s.db.DismissReminder(ctx, actor.ID, kind, scope)
}
