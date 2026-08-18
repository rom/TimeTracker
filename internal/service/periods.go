package service

import (
	"context"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
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

	if err := s.db.UpsertPeriod(ctx, period); err != nil {
		return domain.TimesheetPeriod{}, err
	}
	if err := s.recordAudit(ctx, "timesheet.submit", "timesheet", 0, map[string]any{
		"week_start": period.WeekStart,
		"seconds":    period.SubmittedSeconds,
	}); err != nil {
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

	if err := s.db.UpsertPeriod(ctx, period); err != nil {
		return err
	}
	return s.recordAudit(ctx, "timesheet.withdraw", "timesheet", 0, map[string]any{
		"week_start": period.WeekStart,
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
	if len(reason) > 1000 {
		return fmt.Errorf("%w: the reason is too long", ErrValidation)
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

	if err := s.db.UpsertPeriod(ctx, period); err != nil {
		return err
	}
	return s.recordAudit(ctx, "timesheet."+string(decision), "timesheet", ownerID, map[string]any{
		"week_start": weekStart,
		"user_id":    ownerID,
		"seconds":    period.SubmittedSeconds,
		"reason":     reason,
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

	previousDecider := period.DecidedBy
	period.UserID = ownerID
	period.Status = domain.PeriodOpen
	period.SubmittedAt = time.Time{}
	period.SubmittedSeconds = 0
	period.DecidedBy = actor.ID
	period.DecidedAt = s.now()
	period.Note = reason

	if err := s.db.UpsertPeriod(ctx, period); err != nil {
		return err
	}
	return s.recordAudit(ctx, "timesheet.reopen", "timesheet", ownerID, map[string]any{
		"week_start":             weekStart,
		"user_id":                ownerID,
		"reason":                 reason,
		"previously_approved_by": previousDecider,
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
