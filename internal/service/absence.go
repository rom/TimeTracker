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

// Recording the days somebody was not working.
//
// Two authorities, because there are two kinds of record here. A holiday or a
// sick day is the person's own, recorded by them and visible to them and to
// whoever may already read their timesheet. A public holiday is the instance's:
// it applies to everybody, so recording one is administration
// (docs/adr/0008-rbac-model.md).

// AbsenceInput is what the form supplies.
type AbsenceInput struct {
	Kind     domain.AbsenceKind
	StartsOn string
	EndsOn   string
	Note     string
	// UserID names whose absence it is, for a manager recording somebody
	// else's. Zero means the acting user's own - or, for a public holiday,
	// nobody's in particular.
	UserID int64
}

// RecordAbsence stores a span of days away.
func (s *Service) RecordAbsence(ctx context.Context, in AbsenceInput) (domain.Absence, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.Absence{}, err
	}

	absence := domain.Absence{
		Kind: in.Kind, StartsOn: in.StartsOn, EndsOn: in.EndsOn,
		Note: in.Note, CreatedBy: actor.ID, CreatedAt: s.now(),
	}

	if in.Kind.Everybodys() {
		// A public holiday is the instance's, like a tag or the settings:
		// recording one decides a day off for every person on the instance.
		if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "settings"}); err != nil {
			return domain.Absence{}, notFoundFor(err)
		}
		absence.UserID = 0
	} else {
		absence.UserID = actor.ID
		if in.UserID != 0 && in.UserID != actor.ID {
			if err := s.mayActFor(ctx, in.UserID); err != nil {
				return domain.Absence{}, err
			}
			absence.UserID = in.UserID
		}
	}

	if err := absence.Validate(); err != nil {
		return domain.Absence{}, err
	}

	// Deliberately NOT checked against the week lock. An absence records that
	// somebody was away, which is a fact about the past rather than a figure
	// in the timesheet: it adds no hours, changes no total that was approved,
	// and refusing to record last month's sick day because the week has been
	// signed off would leave the record permanently wrong.
	var created domain.Absence
	err = s.mutate(ctx, "absence.create", "absence", map[string]any{
		"kind": string(absence.Kind), "from": absence.StartsOn,
		"to": absence.EndsOn, "user_id": absence.UserID,
	}, func(tx *sql.Tx) (int64, error) {
		var txErr error
		created, txErr = store.CreateAbsenceTx(ctx, tx, absence)
		return created.ID, txErr
	})
	if err != nil {
		return domain.Absence{}, err
	}
	return created, nil
}

// Absences returns what covers a range for the acting user, public holidays
// included.
func (s *Service) Absences(ctx context.Context, from, to time.Time) ([]domain.Absence, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	// A client records no time and takes no leave here; their own calendar is
	// not this application's business (ADR-0008).
	if actor.Role == domain.RoleClient {
		return nil, nil
	}
	return s.db.ListAbsences(ctx, actor.ID,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
}

// AbsencesForEveryone is the administrator's listing.
func (s *Service) AbsencesForEveryone(ctx context.Context, from, to time.Time) ([]domain.Absence, error) {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "settings"}); err != nil {
		return nil, notFoundFor(err)
	}
	return s.db.ListAbsencesForEveryone(ctx,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
}

// DeleteAbsence removes one, recoverably.
func (s *Service) DeleteAbsence(ctx context.Context, id int64) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	existing, err := s.db.GetAbsence(ctx, id)
	if err != nil {
		return err
	}

	if existing.Kind.Everybodys() {
		if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "settings"}); err != nil {
			return notFoundFor(err)
		}
	} else if existing.UserID != actor.ID {
		if err := s.mayActFor(ctx, existing.UserID); err != nil {
			return err
		}
	}

	summary := fmt.Sprintf("%s · %s–%s", existing.Kind, existing.StartsOn, existing.EndsOn)
	if existing.Note != "" {
		summary += " · " + existing.Note
	}
	return s.binDeletion(ctx, "absence", id, existing.UserID, summary,
		[]store.Capture{{Table: "absences", Where: "id = ?", Args: []any{id}}},
		"absence.delete", "absence", map[string]any{
			"kind": string(existing.Kind), "from": existing.StartsOn,
			"to": existing.EndsOn,
		},
		func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM absences WHERE id = ?`, id)
			return err
		})
}

// absentDaysFor returns the days a person was away in a range, keyed by date.
//
// The one lookup every reader of this shares: the week grid asking about each
// of seven days, the expected-hours arithmetic asking about each working day,
// the reminder asking about the day it was going to nag over.
func (s *Service) absentDaysFor(ctx context.Context, userID int64, from, to time.Time) (map[string]domain.Absence, error) {
	absences, err := s.db.ListAbsences(ctx, userID,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	return domain.AbsentDays(absences), nil
}

// Absence loads one, authorised like deleting it.
func (s *Service) Absence(ctx context.Context, id int64) (domain.Absence, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return domain.Absence{}, err
	}
	absence, err := s.db.GetAbsence(ctx, id)
	if err != nil {
		return domain.Absence{}, err
	}
	// A public holiday is everybody's to read. Anything else is the person's
	// own, or the administrator's to look at on their behalf.
	if !absence.Kind.Everybodys() && absence.UserID != actor.ID {
		if err := s.mayActFor(ctx, absence.UserID); err != nil {
			return domain.Absence{}, err
		}
	}
	return absence, nil
}

// mayActFor decides whether the acting user may touch somebody else's absence.
//
// Administration, and only administration. The first version of this borrowed
// the proxy rule used for recording time on a colleague's behalf, which was
// wrong in a way worth recording: that rule binds a member to the projects
// they share with the subject, and an absence names no project at all. The
// resource therefore reached requireMembership carrying neither a project nor
// a customer nor an id, fell through its last clause, and was allowed - so any
// member could delete any colleague's holiday. It is the same shape as the
// defects fixed when the client role was built: a permission check answers the
// question it was asked, and a record that names no project cannot be bounded
// by project membership.
//
// So absence is bounded by the thing it actually belongs to: a person. Your own
// is yours; anybody else's belongs to whoever administers the instance. A
// manager is not given it, because a manager's authority is scoped by project
// and there is no project here to scope them to.
func (s *Service) mayActFor(ctx context.Context, userID int64) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "user"}); err != nil {
		return notFoundFor(err)
	}
	return nil
}
