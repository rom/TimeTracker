package service

import (
	"context"
	"errors"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// SetTheme stores the acting user's theme choice.
//
// The preference is per user rather than per browser so it follows a person
// between devices in server mode, and so the server can apply the right theme
// during the first render instead of the page flashing the default before
// JavaScript corrects it.
func (s *Service) SetTheme(ctx context.Context, theme string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionUpdate, auth.Resource{
		Type: "user", ID: actor.ID, OwnerID: actor.ID,
	}); err != nil {
		return err
	}
	// Not audited: a theme choice is a display preference with no bearing on
	// billing or access. Auditing it would bury the events that matter.
	return s.db.UpdateUserPreferences(ctx, actor.ID, theme, actor.TimeZone)
}

// SetTimeZone stores the acting user's time zone.
//
// This one does matter: the zone decides which calendar day an entry belongs to,
// and therefore which invoice it lands on. Existing entries keep the zone they
// were recorded in, so changing this never moves historical time between days.
func (s *Service) SetTimeZone(ctx context.Context, timeZone string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	if err := s.authz.Can(ctx, auth.ActionUpdate, auth.Resource{
		Type: "user", ID: actor.ID, OwnerID: actor.ID,
	}); err != nil {
		return err
	}
	if err := s.db.UpdateUserPreferences(ctx, actor.ID, actor.Theme, timeZone); err != nil {
		return err
	}
	return s.recordAudit(ctx, "user.set_time_zone", "user", actor.ID, map[string]any{
		"from": actor.TimeZone, "to": timeZone,
	})
}

// EnsureLocalUser returns the single local-mode user, creating it on first run.
//
// Local mode has no sign-up: the person who launched the process is the user.
// This is the only place a user is created without an administrator, and it is
// reachable only from the local-mode start-up path.
func (s *Service) EnsureLocalUser(ctx context.Context, displayName, timeZone string) error {
	_, err := s.db.FirstUser(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err = s.db.CreateUser(ctx, localUser(displayName, timeZone))
	return err
}

// LocalUser loads the single local-mode identity, for the request middleware.
func (s *Service) LocalUser(ctx context.Context) (domain.User, error) {
	return s.db.FirstUser(ctx)
}

// localUser builds the record created on a first local-mode run.
func localUser(displayName, timeZone string) domain.User {
	if displayName == "" {
		displayName = "Me"
	}
	if timeZone == "" {
		timeZone = "UTC"
	}
	return domain.User{
		DisplayName: displayName,
		// Local mode has one user who owns everything in their own database, so
		// they hold the admin role. The authoriser is still consulted on every
		// action - the role is data, not a bypass.
		Role:     domain.RoleAdmin,
		TimeZone: timeZone,
		Theme:    "light",
		Active:   true,
	}
}
