package service

import (
	"context"
	"time"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// The running over or under.
//
// Expected hours answer one week at a time: this week is 37h 30m of 40h, so
// two and a half hours short. The question that follows immediately is the one
// flexitime actually turns on - am I up or down overall - and until this
// existed nothing could answer it.
//
// Three decisions worth stating, because each is the difference between a
// figure people trust and one they argue with.
//
// It is derived, not stored. A balance column would need every write to
// remember to adjust it, and would drift the first time one forgot; this way
// correcting last March corrects the balance.
//
// It counts whole weeks only. A balance that included the week in progress
// would read as thirty-two hours down on a Monday morning, every Monday, which
// is both true and useless: the week has not happened yet. The current week is
// shown beside the balance as its own figure, where it belongs.
//
// It is off unless somebody turns it on. A running figure is read as hours
// owed, in both directions, by people who did not choose to be measured that
// way - so the instance says when the count starts, or no balance is shown at
// all.

// Balance is a person's accumulated over-or-under across whole weeks.
type Balance struct {
	// Seconds is positive when they are ahead and negative when behind.
	Seconds int64
	// Weeks is how many whole weeks it covers, and From and To are the first
	// and last of them. A figure with no stated range is one nobody can check.
	Weeks int
	From  time.Time
	To    time.Time
	// WorkedSeconds and AwaySeconds are what produced it, so the screen can
	// say where the number came from rather than asserting it.
	WorkedSeconds int64
	AwaySeconds   int64
	// ExpectedSeconds is what those weeks asked for.
	ExpectedSeconds int64
}

// Counted reports whether there is a balance to show.
func (b Balance) Counted() bool { return b.Weeks > 0 }

// balanceWith works out the over-or-under for one person up to the week that
// contains the given day, excluding that week.
//
// Deliberately unexported, and deliberately taking the owner rather than an
// id. It asks the authoriser nothing, because it is only ever reached from a
// view that has already established whose week or month this is and whether
// the actor may read it - the week view and the month view. An exported
// BalanceFor(ctx, user) would be a way to read any colleague's hours by
// passing their record, which is the shape of the defect the absence work
// found: a method that answers the question it was asked rather than the one
// the caller is entitled to ask.
func (s *Service) balanceWith(ctx context.Context, settings store.Settings, owner domain.User, upTo time.Time) (Balance, error) {
	expectedWeek := int64(expectedMinutesFor(settings, owner)) * 60
	if expectedWeek <= 0 {
		// Nothing to measure against, so there is nothing to accumulate.
		return Balance{}, nil
	}

	loc := locationFor(owner)
	start, ok := balanceStartFor(settings, owner, loc)
	if !ok {
		return Balance{}, nil
	}

	// Whole weeks, aligned to the instance's first day: the first week counted
	// is the one the start date falls in, and the last is the one before the
	// week being looked at.
	firstWeek, err := domain.ParseWeekStart(
		domain.WeekStartFor(start, settings.WeekStart, loc), loc)
	if err != nil {
		return Balance{}, nil
	}
	currentWeek, err := domain.ParseWeekStart(
		domain.WeekStartFor(upTo, settings.WeekStart, loc), loc)
	if err != nil {
		return Balance{}, nil
	}

	weeks := int(currentWeek.Sub(firstWeek) / (7 * 24 * time.Hour))
	if weeks <= 0 {
		// The balance begins this week, so there is nothing behind it yet.
		return Balance{}, nil
	}

	const iso = "2006-01-02"
	worked, err := s.db.RangeSeconds(ctx, owner.ID,
		firstWeek.Format(iso), currentWeek.Format(iso))
	if err != nil {
		return Balance{}, err
	}

	// Days away fill their share, exactly as they do in one week: a fortnight
	// off is two complete weeks, not eighty hours owed.
	away, err := s.absentDaysFor(ctx, owner.ID, firstWeek, currentWeek.AddDate(0, 0, -1))
	if err != nil {
		return Balance{}, err
	}
	awayDays := 0
	for day := firstWeek; day.Before(currentWeek); day = day.AddDate(0, 0, 1) {
		if domain.IsWeekend(day) {
			continue
		}
		if _, off := away[day.Format(iso)]; off {
			awayDays++
		}
	}

	// A working week is five days, which is what makes one day away worth a
	// fifth of it. The same share the week view uses.
	const workingDaysInWeek = 5
	balance := Balance{
		Weeks:           weeks,
		From:            firstWeek,
		To:              currentWeek.AddDate(0, 0, -1),
		WorkedSeconds:   worked,
		AwaySeconds:     int64(awayDays) * expectedWeek / workingDaysInWeek,
		ExpectedSeconds: int64(weeks) * expectedWeek,
	}
	balance.Seconds = balance.WorkedSeconds + balance.AwaySeconds - balance.ExpectedSeconds
	return balance, nil
}

// balanceStartFor says when the count begins, and whether it begins at all.
//
// The person's own date where they have one, which is how somebody who joined
// in June is not born owing five months. Theirs may only be later than the
// instance's: an account cannot award itself a longer run at the figure than
// the instance is counting.
func balanceStartFor(settings store.Settings, owner domain.User, loc *time.Location) (time.Time, bool) {
	instance, instanceOK := parseBalanceDate(settings.BalanceStartsOn, loc)
	if !instanceOK {
		// The instance has not turned the balance on, so nobody has one.
		return time.Time{}, false
	}
	if mine, ok := parseBalanceDate(owner.BalanceStartsOn, loc); ok && mine.After(instance) {
		return mine, true
	}
	return instance, true
}

// parseBalanceDate reads a stored ISO date, treating anything unreadable as
// absent rather than as the zero time - which would silently count from the
// year one.
func parseBalanceDate(value string, loc *time.Location) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, loc)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
