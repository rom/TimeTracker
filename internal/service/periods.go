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

// Weekly submit and approve.
//
// The workflow is deliberately short:
//
//	open ──submit──▶ submitted ──approve──▶ approved
//	  ▲                  │                      │
//	  └──withdraw────────┘                      │
//	  ◀──────────reject (with a reason)─────────┤
//	  ◀──────────reopen (deliberate)────────────┘
//
// Three rules give it its shape:
//
//   - Submitting locks the week to its owner. That is the point of submitting:
//     it says "these are my hours", and hours that keep changing afterwards are
//     not a declaration.
//   - Only a manager approves, and never their own week. Approving your own
//     timesheet is not approval.
//   - An approved week can be reopened, but it takes a deliberate act by a
//     manager and it is audited. Sometimes an approved timesheet is simply
//     wrong, and a system with no way back forces people to correct it by
//     inventing an adjustment somewhere else.

// PeriodView is a week together with what the acting user may do to it.
type PeriodView struct {
	Period domain.TimesheetPeriod
	// TotalSeconds is the week as it stands now.
	TotalSeconds int64
	// ExpectedSeconds is a full week for whoever's week this is, or zero where
	// the instance has turned the comparison off. A total with nothing to
	// compare it against cannot answer the question people open a timesheet to
	// answer on a Thursday afternoon.
	ExpectedSeconds int64
	// AbsentSeconds is the share of the expected week that recorded absence
	// accounts for, and AbsentDays how many working days produced it. A week
	// off is a complete week, not a week somebody failed to fill in.
	AbsentSeconds int64
	AbsentDays    int
	// CanSubmit, CanWithdraw, CanApprove and CanReopen say which controls to
	// offer. The template asks these rather than re-deriving the rules, so the
	// interface and the enforcement cannot disagree.
	CanSubmit   bool
	CanWithdraw bool
	CanApprove  bool
	CanReopen   bool
}

// Period returns the state of one week for the acting user.
func (s *Service) Period(ctx context.Context, date time.Time) (PeriodView, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return PeriodView{}, err
	}
	return s.periodFor(ctx, actor, actor.ID, date)
}

// periodFor loads a week and works out what the actor may do to it.
func (s *Service) periodFor(ctx context.Context, actor domain.User, ownerID int64, date time.Time) (PeriodView, error) {
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return PeriodView{}, err
	}
	owner := actor
	if ownerID != actor.ID {
		if owner, err = s.db.GetUser(ctx, ownerID); err != nil {
			return PeriodView{}, err
		}
	}

	weekStart := domain.WeekStartFor(date, settings.WeekStart, locationFor(owner))
	period, err := s.db.GetPeriod(ctx, ownerID, weekStart)
	if err != nil {
		return PeriodView{}, err
	}

	total, err := s.db.WeekSeconds(ctx, ownerID, weekStart)
	if err != nil {
		return PeriodView{}, err
	}
	period.CurrentSeconds = total

	view := PeriodView{Period: period, TotalSeconds: total}

	// How much a full week is for this person, so the total has something to
	// be measured against. The owner's own figure where they have one, since
	// part time is a property of the person - and the owner rather than the
	// reader, because a manager looking at somebody's week wants to know
	// whether THEY are short, not whether the manager would be.
	view.ExpectedSeconds = int64(expectedMinutesFor(settings, owner)) * 60

	// Days away fill their share of it, so a week's holiday reads as a
	// complete week rather than as a failure to fill the timesheet in. The
	// share is the week divided by its working days, which is what makes one
	// day of leave worth a fifth of a Monday-to-Friday week without anybody
	// storing a daily figure.
	if view.ExpectedSeconds > 0 {
		start, parseErr := domain.ParseWeekStart(weekStart, locationFor(owner))
		if parseErr == nil {
			away, awayErr := s.absentDaysFor(ctx, ownerID, start, start.AddDate(0, 0, 6))
			if awayErr != nil {
				return PeriodView{}, awayErr
			}
			working, absent := 0, 0
			for offset := range 7 {
				day := start.AddDate(0, 0, offset)
				if domain.IsWeekend(day) {
					continue
				}
				working++
				if _, off := away[day.Format("2006-01-02")]; off {
					absent++
				}
			}
			if working > 0 && absent > 0 {
				view.AbsentSeconds = view.ExpectedSeconds * int64(absent) / int64(working)
				view.AbsentDays = absent
			}
		}
	}

	own := ownerID == actor.ID
	// An empty week is not worth submitting, and submitting one would put an
	// item in a manager's queue with nothing in it to look at.
	view.CanSubmit = own && !period.Locked() && total > 0
	view.CanWithdraw = own && period.Status == domain.PeriodSubmitted

	// Approval needs the manage permission and is never available on your own
	// week, whatever your role. An administrator working on a project still
	// cannot sign off their own hours.
	canManage := s.authz.Can(ctx, auth.ActionApprove, auth.Resource{
		Type: "timesheet", OwnerID: ownerID,
	}) == nil
	view.CanApprove = canManage && !own && period.Status == domain.PeriodSubmitted
	view.CanReopen = canManage && !own && period.Status == domain.PeriodApproved

	return view, nil
}

// SubmitWeek declares a week finished.
func (s *Service) SubmitWeek(ctx context.Context, date time.Time) (domain.TimesheetPeriod, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.TimesheetPeriod{}, err
	}

	view, err := s.periodFor(ctx, actor, actor.ID, date)
	if err != nil {
		return domain.TimesheetPeriod{}, err
	}
	if view.Period.Locked() {
		return domain.TimesheetPeriod{}, fmt.Errorf(
			"%w: this week has already been %s", ErrConflict, view.Period.Status)
	}
	if view.TotalSeconds == 0 {
		return domain.TimesheetPeriod{}, fmt.Errorf(
			"%w: there is nothing recorded in this week to submit", ErrValidation)
	}
	// A customer that requires receipts above an amount requires them before the
	// claim is made, not after it has been invoiced. This is the first point at
	// which the requirement can be enforced: an attachment needs an expense to
	// belong to, so it cannot be demanded when the expense is created.
	if err := s.checkReceiptsPresent(ctx, actor.ID, view.Period.WeekStart, locationFor(actor)); err != nil {
		return domain.TimesheetPeriod{}, err
	}

	period := view.Period
	period.UserID = actor.ID
	period.Status = domain.PeriodSubmitted
	period.SubmittedAt = s.now()
	period.SubmittedSeconds = view.TotalSeconds
	// A resubmission after a rejection starts clean: the previous reason no
	// longer describes the week that is now being offered.
	period.Note = ""
	period.DecidedBy = 0
	period.DecidedAt = time.Time{}

	err = s.mutate(ctx, "timesheet.submit", "timesheet", map[string]any{
		"week_start": period.WeekStart,
		"seconds":    period.SubmittedSeconds,
	}, func(tx *sql.Tx) (int64, error) {
		return 0, store.UpsertPeriodTx(ctx, tx, period)
	})
	if err != nil {
		return domain.TimesheetPeriod{}, err
	}
	return period, nil
}

// WithdrawWeek takes back a submission that has not been decided.
//
// Its own operation rather than a rejection of oneself: people submit a week and
// then remember something, and needing a manager to undo that would make them
// stop submitting on time.
func (s *Service) WithdrawWeek(ctx context.Context, date time.Time) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	view, err := s.periodFor(ctx, actor, actor.ID, date)
	if err != nil {
		return err
	}
	if view.Period.Status != domain.PeriodSubmitted {
		return fmt.Errorf("%w: only a submitted week can be withdrawn", ErrConflict)
	}

	period := view.Period
	period.Status = domain.PeriodOpen
	period.SubmittedAt = time.Time{}
	period.SubmittedSeconds = 0

	return s.mutate(ctx, "timesheet.withdraw", "timesheet", map[string]any{
		"week_start": period.WeekStart,
	}, func(tx *sql.Tx) (int64, error) {
		return 0, store.UpsertPeriodTx(ctx, tx, period)
	})
}

// ApproveWeek accepts somebody else's submitted week.
func (s *Service) ApproveWeek(ctx context.Context, ownerID int64, weekStart string) error {
	return s.decideWeek(ctx, ownerID, weekStart, domain.PeriodApproved, "")
}

// RejectWeek sends a week back with a reason.
//
// The reason is required. "Rejected" with no explanation leaves the owner
// guessing at what to change, and they will simply resubmit the same hours.
func (s *Service) RejectWeek(ctx context.Context, ownerID int64, weekStart, reason string) error {
	if len(reason) == 0 {
		return fmt.Errorf("%w: say why, so the week can be corrected", ErrValidation)
	}
	if err := checkReason(reason); err != nil {
		return err
	}
	return s.decideWeek(ctx, ownerID, weekStart, domain.PeriodRejected, reason)
}

// decideWeek is the shared approve/reject path.
func (s *Service) decideWeek(ctx context.Context, ownerID int64, weekStart string, decision domain.PeriodStatus, reason string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	// Approving your own timesheet is not approval, whatever your role.
	if ownerID == actor.ID {
		return fmt.Errorf("%w: you cannot decide on your own timesheet", ErrValidation)
	}
	if err := s.authz.Can(ctx, auth.ActionApprove, auth.Resource{
		Type: "timesheet", OwnerID: ownerID,
	}); err != nil {
		return notFoundFor(err)
	}

	period, err := s.db.GetPeriod(ctx, ownerID, weekStart)
	if err != nil {
		return err
	}
	if period.Status != domain.PeriodSubmitted {
		return fmt.Errorf("%w: this week is %s, not awaiting a decision",
			ErrConflict, period.Status)
	}

	period.UserID = ownerID
	period.Status = decision
	period.DecidedBy = actor.ID
	period.DecidedAt = s.now()
	period.Note = reason
	// A rejection reopens the week, so the owner can correct it.
	if decision == domain.PeriodRejected {
		period.SubmittedAt = time.Time{}
	}

	return s.mutate(ctx, "timesheet."+string(decision), "timesheet", map[string]any{
		"week_start": weekStart,
		"user_id":    ownerID,
		"seconds":    period.SubmittedSeconds,
		"reason":     reason,
	}, func(tx *sql.Tx) (int64, error) {
		return ownerID, store.UpsertPeriodTx(ctx, tx, period)
	})
}

// ReopenWeek unlocks an approved week.
//
// Deliberately a separate operation with its own audit action. Sometimes an
// approved timesheet is simply wrong, and a system with no way back forces
// people to correct it by inventing an adjustment elsewhere - which is worse
// than an audited reopening.
func (s *Service) ReopenWeek(ctx context.Context, ownerID int64, weekStart, reason string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionApprove, auth.Resource{
		Type: "timesheet", OwnerID: ownerID,
	}); err != nil {
		return notFoundFor(err)
	}

	period, err := s.db.GetPeriod(ctx, ownerID, weekStart)
	if err != nil {
		return err
	}
	if period.Status != domain.PeriodApproved {
		return fmt.Errorf("%w: only an approved week needs reopening", ErrConflict)
	}
	// Reopening an approved week is the most consequential of the four, and was
	// the other one with no bound on its explanation.
	if err := checkReason(reason); err != nil {
		return err
	}

	previousDecider := period.DecidedBy
	period.UserID = ownerID
	period.Status = domain.PeriodOpen
	period.SubmittedAt = time.Time{}
	period.SubmittedSeconds = 0
	period.DecidedBy = actor.ID
	period.DecidedAt = s.now()
	period.Note = reason

	return s.mutate(ctx, "timesheet.reopen", "timesheet", map[string]any{
		"week_start":             weekStart,
		"user_id":                ownerID,
		"reason":                 reason,
		"previously_approved_by": previousDecider,
	}, func(tx *sql.Tx) (int64, error) {
		return ownerID, store.UpsertPeriodTx(ctx, tx, period)
	})
}

// PendingApprovals returns the weeks awaiting the acting user's decision.
func (s *Service) PendingApprovals(ctx context.Context) ([]domain.TimesheetPeriod, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Can(ctx, auth.ActionApprove, auth.Resource{Type: "timesheet"}); err != nil {
		// Not an error: someone without the permission simply has an empty
		// queue, and the screen says so rather than refusing to load.
		return nil, nil
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	periods, err := s.db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted, scope)
	if err != nil {
		return nil, err
	}

	// Nobody approves their own week, so it does not belong in their queue.
	var queue []domain.TimesheetPeriod
	for _, period := range periods {
		if period.UserID == actor.ID {
			continue
		}
		// The week as it stands now, so a manager can see whether it has moved
		// since submission before signing it off.
		if period.CurrentSeconds, err = s.db.WeekSeconds(ctx, period.UserID, period.WeekStart); err != nil {
			return nil, err
		}
		queue = append(queue, period)
	}
	return queue, nil
}

// ApprovedPeriods lists weeks the acting user has the standing to reopen.
//
// Reopening is only useful if an approved week can be found, and an approved
// week leaves the pending queue the moment it is decided. Without this list the
// operation would exist in the service and be unreachable from the screen.
func (s *Service) ApprovedPeriods(ctx context.Context) ([]domain.TimesheetPeriod, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Can(ctx, auth.ActionApprove, auth.Resource{Type: "timesheet"}); err != nil {
		return nil, nil
	}

	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	periods, err := s.db.ListPeriodsByStatus(ctx, domain.PeriodApproved, scope)
	if err != nil {
		return nil, err
	}

	var decided []domain.TimesheetPeriod
	for _, period := range periods {
		// Nobody reopens their own week, for the same reason nobody approves it.
		if period.UserID == actor.ID {
			continue
		}
		decided = append(decided, period)
	}
	return decided, nil
}

// RecentPeriods lists the acting user's own weeks, for a history panel.
func (s *Service) RecentPeriods(ctx context.Context) ([]domain.TimesheetPeriod, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	return s.db.ListPeriodsForUser(ctx, actor.ID, 12)
}

// ---------------------------------------------------------------- locking ---

// checkWeekendAllowed refuses new time that would land on a weekend, when the
// instance says weekends are not worked.
//
// Deliberately NOT folded into checkPeriodOpen, although the two look alike and
// are called from the same places. checkPeriodOpen's callers include deleting
// an entry and stopping a running timer, and those must not be refused here: an
// entry already sitting on a Saturday - recorded before the policy was turned
// on, imported from a calendar, or left running past midnight on Friday - would
// become impossible to stop, correct or remove, which would strand the data the
// policy was meant to discourage. The lock and the policy answer different
// questions ("has this week been signed off" and "should this day be worked
// at all"), so they are different functions with different call sites.
//
// It governs where time arrives, not where it already is: creating an entry,
// starting a timer, and moving an existing entry onto a weekend. Moving one
// OFF a weekend, editing it in place, and deleting it are all untouched.
//
// The day is judged in the owner's zone, like everything else about which day
// an entry belongs to: 22:00 on Friday in Stockholm is Friday for a Swedish
// employee whatever the server thinks.
func (s *Service) checkWeekendAllowed(ctx context.Context, userID int64, when time.Time) error {
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	if settings.WeekendRecording {
		return nil
	}
	owner, err := s.db.GetUser(ctx, userID)
	if err != nil {
		// A user that cannot be loaded is not a reason to allow the change.
		return err
	}

	local := when.In(locationFor(owner))
	if !domain.IsWeekend(local) {
		return nil
	}
	// Named rather than "the weekend", because the refusal is read by somebody
	// who believes they picked a working day, and the day they actually picked
	// is the useful half of the answer.
	return fmt.Errorf("%w: %s is a weekend, and this instance does not record time on weekends",
		ErrValidation, local.Format("Monday 2 January 2006"))
}

// checkPeriodOpen refuses a change that falls inside a locked week.
//
// Every mutation of a time entry goes through here. That is the whole mechanism:
// there is one function, and a new way to change an entry has to call it or the
// lock does not exist.
//
// An administrator is not exempt. A lock that the most privileged user silently
// bypasses is not a lock, and the reopen operation exists precisely so that
// unlocking is a visible, audited act rather than a side effect of who you are.
func (s *Service) checkPeriodOpen(ctx context.Context, userID int64, when time.Time) error {
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	owner, err := s.db.GetUser(ctx, userID)
	if err != nil {
		// A user that cannot be loaded is not a reason to allow the change.
		return err
	}

	weekStart := domain.WeekStartFor(when, settings.WeekStart, locationFor(owner))
	period, err := s.db.GetPeriod(ctx, userID, weekStart)
	if err != nil {
		return err
	}
	if !period.Locked() {
		return nil
	}

	// The message says what to do about it, because "locked" alone leaves
	// someone stuck: a submission can be withdrawn by its owner, an approval
	// needs a manager.
	if period.Status == domain.PeriodSubmitted {
		return fmt.Errorf("%w: the week of %s has been submitted; withdraw it to make changes",
			domain.ErrPeriodLocked, weekStart)
	}
	return fmt.Errorf("%w: the week of %s has been approved; ask a manager to reopen it",
		domain.ErrPeriodLocked, weekStart)
}

// expectedMinutesFor resolves one person's full week.
//
// The owner's own figure where they have set one, the instance's otherwise.
// Negative means "no preference"; zero is a deliberate choice meaning "do not
// compare", which an instance that would rather not put a target on a
// timesheet can set once.
func expectedMinutesFor(settings store.Settings, owner domain.User) int {
	// Strictly positive, not merely non-negative. The column stores -1 for
	// "no preference", but a domain.User is also built in Go in a dozen
	// places - CreateUser's return value, a fixture, a restored backup - and
	// there the field is the Go zero value. Treating zero as a deliberate
	// "do not compare" made every one of those silently turn the comparison
	// off: the first test written against this reported a full week of zero
	// seconds on a fresh instance whose setting plainly said forty hours.
	//
	// So zero from a person means "I have not said", and turning the
	// comparison off is the instance's decision, made once, for everybody.
	if owner.ExpectedWeeklyMinutes > 0 {
		return owner.ExpectedWeeklyMinutes
	}
	if settings.ExpectedWeeklyMinutes < 0 {
		return 0
	}
	return settings.ExpectedWeeklyMinutes
}
