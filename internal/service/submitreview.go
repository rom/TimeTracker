package service

import (
	"context"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Reviewing a week before submitting it.
//
// Submitting is a statement about specific figures: it closes the week to
// changes and puts it in somebody else's queue to sign off. The refusals that
// enforce that are in SubmitWeek, and by the time one of them fires the person
// has already pressed the button - which is the wrong moment to be told that
// Thursday is empty or that a receipt is missing.
//
// So this is the same set of questions asked before the button, as a read. It
// writes nothing and decides nothing: every figure it shows is read back from
// the same functions the week screen uses, and every objection it raises is
// one SubmitWeek would raise on its own. Keeping the two in step is the point -
// a review that said "ready to submit" and then a submission that refused
// would be worse than no review at all, which is why the test for this walks
// both paths over the same week.

// SubmitReview is what somebody sees before submitting a week.
type SubmitReview struct {
	// Week is the week being reviewed, with its totals and its rows, exactly
	// as the week screen shows them.
	Week WeekView
	// Period carries the week's current state and what may be done to it.
	Period PeriodView

	// Blockers are the reasons this week cannot be submitted at all. Each is a
	// refusal SubmitWeek itself would make, asked here first.
	Blockers []SubmitNote
	// Warnings are things worth looking at that do not stop a submission: an
	// empty weekday, an unusually long day, time still running.
	Warnings []SubmitNote

	// Expenses recorded in the week, because they are submitted with it and a
	// claim nobody looked at is the one that comes back.
	Expenses []domain.Expense
	// ExpenseTotals summarises them in the same shape the expenses screen uses.
	ExpenseTotals ExpenseTotals
	// NeedsReceipt names the expenses whose customer's contract requires
	// evidence they do not have. These are blockers, and they are listed
	// per expense so the screen can point at the row rather than at the week.
	NeedsReceipt map[int64]bool

	// Flagged are entries marked for review, which count towards nothing until
	// somebody resolves them - so a week whose total looks right can still be
	// short by whatever is in here.
	Flagged []domain.TimeEntry
	// Pending are entries somebody else recorded on this person's behalf and
	// which they have not decided about. They are not part of the submission
	// and will not be, which is worth saying before rather than after.
	Pending []domain.TimeEntry
	// Running are timers still going. Submitting leaves them out, because an
	// entry with no end has no length.
	Running []domain.TimeEntry
}

// SubmitNote is one thing to say about a week being reviewed.
//
// Code rather than a sentence, so the screen translates it: these appear in
// front of somebody who chose their language, and a server-side English string
// would be the one untranslated thing on the page.
type SubmitNote struct {
	// Code names the kind of note, for the catalogue key and for a test to
	// assert against without matching prose.
	Code string
	// Day is the day it is about, where it is about one.
	Day time.Time
	// Count is how many of whatever it is, where that is the useful figure.
	Count int
	// Seconds is the duration it is about, where it is about one.
	Seconds int64
}

// Note codes. Each maps onto a catalogue key, and the blockers each correspond
// to a refusal in SubmitWeek.
const (
	// SubmitBlockedLocked is a week already submitted or approved.
	SubmitBlockedLocked = "locked"
	// SubmitBlockedEmpty is a week with nothing recorded in it. Submitting one
	// would put an item in a manager's queue with nothing to look at.
	SubmitBlockedEmpty = "empty"
	// SubmitBlockedReceipts is an expense whose contract requires a receipt it
	// does not have.
	SubmitBlockedReceipts = "receipts"
	// SubmitWarnEmptyDay is a weekday with nothing on it. Usually a day
	// somebody forgot rather than a day they did not work.
	SubmitWarnEmptyDay = "empty_day"
	// SubmitWarnFlagged is an entry marked for review, which counts towards
	// nothing until it is resolved.
	SubmitWarnFlagged = "flagged"
	// SubmitWarnRunning is a timer still going, which is not submitted.
	SubmitWarnRunning = "running"
	// SubmitWarnPending is a proposal awaiting this person's decision, which is
	// not submitted either.
	SubmitWarnPending = "pending"
	// SubmitWarnLongDay is a day over the configured timer ceiling, which is
	// more often a timer left running than a day somebody worked.
	SubmitWarnLongDay = "long_day"
)

// ReviewSubmission assembles the week and everything worth knowing before it is
// submitted.
//
// It takes any date in the week, like every other weekly read, so a link from
// the day screen does not have to work out which week it is in.
func (s *Service) ReviewSubmission(ctx context.Context, date time.Time) (SubmitReview, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return SubmitReview{}, err
	}

	week, err := s.Week(ctx, date)
	if err != nil {
		return SubmitReview{}, err
	}
	period, err := s.Period(ctx, date)
	if err != nil {
		return SubmitReview{}, err
	}

	review := SubmitReview{Week: week, Period: period, NeedsReceipt: map[int64]bool{}}

	// Blockers first, each one a refusal SubmitWeek would make.
	if period.Period.Locked() {
		review.Blockers = append(review.Blockers, SubmitNote{Code: SubmitBlockedLocked})
	}
	if week.Totals.SummedSeconds == 0 {
		review.Blockers = append(review.Blockers, SubmitNote{Code: SubmitBlockedEmpty})
	}

	loc := locationFor(actor)
	settings, err := s.Settings(ctx)
	if err != nil {
		return SubmitReview{}, err
	}

	// The week's expenses, which are submitted with it.
	weekStart, weekEnd := week.Start, week.Start.AddDate(0, 0, 7)
	review.Expenses, err = s.Expenses(ctx, ExpenseFilter{
		UserID: actor.ID, From: weekStart, To: weekEnd,
	})
	if err != nil {
		return SubmitReview{}, err
	}
	review.ExpenseTotals = s.ExpenseTotals(review.Expenses)

	missingReceipts := 0
	for _, expense := range review.Expenses {
		needs, err := s.NeedsReceipt(ctx, expense)
		if err != nil {
			return SubmitReview{}, err
		}
		if needs {
			review.NeedsReceipt[expense.ID] = true
			missingReceipts++
		}
	}
	if missingReceipts > 0 {
		review.Blockers = append(review.Blockers, SubmitNote{
			Code: SubmitBlockedReceipts, Count: missingReceipts,
		})
	}

	// Then the warnings, in the order somebody would read them: what is wrong
	// with the week as a whole, then what is wrong with particular entries.
	//
	// Nothing per-day on a week that is empty. Five "Monday is empty" notes
	// under a "this week is empty" blocker say nothing the blocker did not,
	// and they bury it.
	perDay := week.Totals.SummedSeconds > 0
	for i, day := range week.Days {
		if !perDay {
			break
		}
		// Weekdays only. A quiet Saturday is not an omission.
		if weekday := day.In(loc).Weekday(); weekday == time.Saturday || weekday == time.Sunday {
			continue
		}
		var seconds int64
		if i < len(week.DayTotals) {
			seconds = week.DayTotals[i].SummedSeconds
		}
		if seconds == 0 {
			review.Warnings = append(review.Warnings, SubmitNote{
				Code: SubmitWarnEmptyDay, Day: day,
			})
			continue
		}
		if settings.MaxTimerSeconds > 0 && seconds > settings.MaxTimerSeconds {
			review.Warnings = append(review.Warnings, SubmitNote{
				Code: SubmitWarnLongDay, Day: day, Seconds: seconds,
			})
		}
	}

	entries, err := s.Entries(ctx, EntryFilter{
		UserID: actor.ID, From: weekStart, To: weekEnd,
	})
	if err != nil {
		return SubmitReview{}, err
	}
	for _, entry := range entries {
		switch {
		case entry.Running():
			review.Running = append(review.Running, entry)
		case entry.Status == domain.StatusPending:
			review.Pending = append(review.Pending, entry)
		case entry.Flagged:
			review.Flagged = append(review.Flagged, entry)
		}
	}
	for _, group := range []struct {
		code    string
		entries []domain.TimeEntry
	}{
		{SubmitWarnFlagged, review.Flagged},
		{SubmitWarnRunning, review.Running},
		{SubmitWarnPending, review.Pending},
	} {
		if len(group.entries) > 0 {
			var seconds int64
			for _, entry := range group.entries {
				seconds += entry.DurationSeconds
			}
			review.Warnings = append(review.Warnings, SubmitNote{
				Code: group.code, Count: len(group.entries), Seconds: seconds,
			})
		}
	}

	return review, nil
}

// Ready reports whether the week can be submitted as it stands.
//
// Warnings deliberately do not stop it: an empty Friday is somebody's half-day
// as often as it is an omission, and a review that refused to let them past it
// would be a review they learn to click through.
func (r SubmitReview) Ready() bool { return len(r.Blockers) == 0 && r.Period.CanSubmit }

// Any reports whether there is anything at all to say about this week, so a
// screen can show a plain "this looks complete" rather than an empty list.
func (r SubmitReview) Any() bool { return len(r.Blockers) > 0 || len(r.Warnings) > 0 }

// MissingReceipts is how many expenses in the week lack evidence their
// customer's contract requires.
func (r SubmitReview) MissingReceipts() int { return len(r.NeedsReceipt) }
