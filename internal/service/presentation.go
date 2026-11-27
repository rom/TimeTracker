package service

import (
	"context"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// What somebody chooses for themselves about how the application looks.
//
// Theme, zone and language were always the account's. These six were the
// instance's, settable only by an administrator, which on a server meant one
// person picked everybody's working hours and date format. They are personal
// now, with the instance's values as the default: an empty choice means "leave
// it to the instance", so nothing changes for anybody until they choose.
//
// The week's first day is not here and will not be. It decides which Monday a
// timesheet period is keyed by, so two people disagreeing about it would be two
// people disagreeing about which week was approved
// (docs/adr/0023-week-as-the-unit-of-approval.md).

// PresentationInput is the personal half of the settings form.
//
// Every field is "unset means follow the instance". The hours are a pair:
// either both are given or neither is, because half a window is not a window.
type PresentationInput struct {
	ClockFormat string
	DateFormat  string
	NavPosition string
	DayOverflow string
	// DayStartHour and DayEndHour are negative when the person has expressed
	// no preference.
	DayStartHour int
	DayEndHour   int
	// ExpectedWeeklyMinutes is this person's full week, or negative to follow
	// the instance.
	ExpectedWeeklyMinutes int
}

// UpdatePresentation saves the acting user's own presentation choices.
//
// No authorisation check beyond having an identity: this writes nothing but the
// acting user's own row, and a person is always allowed to decide how their own
// screen looks. The values are reduced to ones the stylesheet and the printer
// have rules for, so an edited form cannot put a mystery string in a class
// attribute or a format nothing renders.
func (s *Service) UpdatePresentation(ctx context.Context, in PresentationInput) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	chosen := domain.User{
		ClockFormat:  keepKnown(in.ClockFormat, knownClockFormats()),
		DateFormat:   keepKnown(in.DateFormat, knownDateFormats()),
		NavPosition:  keepKnown(in.NavPosition, knownNavPositions()),
		DayOverflow:  keepKnown(in.DayOverflow, knownDayOverflows()),
		DayStartHour: -1,
		DayEndHour:   -1,
		// A week longer than seven days of hours is a typo, and a negative one
		// is "no preference". Clamped rather than refused, like every other
		// presentation value on this form.
		ExpectedWeeklyMinutes: -1,
	}
	if in.ExpectedWeeklyMinutes >= 0 && in.ExpectedWeeklyMinutes <= 7*24*60 {
		chosen.ExpectedWeeklyMinutes = in.ExpectedWeeklyMinutes
	}
	// Both or neither. Taking one of somebody's hours and one of the
	// instance's could produce an end before a start, which the normaliser
	// would then silently swap - a sensible thing to do with a typo and a
	// confusing thing to do with a preference.
	if in.DayStartHour >= 0 && in.DayEndHour >= 0 {
		window := domain.NormaliseDayWindow(in.DayStartHour, in.DayEndHour)
		chosen.DayStartHour, chosen.DayEndHour = window.StartHour, window.EndHour
	}

	if err := s.db.UpdateUserPresentation(ctx, actor.ID, chosen); err != nil {
		return err
	}
	// Audited like any other change to a stored record. Quieter than the
	// instance settings - nobody else is affected - but a screen that
	// mysteriously renders differently is a support question, and the trail is
	// what answers it.
	s.auditLog(ctx, "user.presentation", "user", actor.ID)
	return nil
}

// keepKnown returns value if it is one of the known ones, and "" otherwise.
//
// Empty is the meaningful default here rather than a fallback: it is how a
// person says "no preference", so an unrecognised value degrades to following
// the instance rather than to some other arbitrary choice.
func keepKnown(value string, known []string) string {
	for _, candidate := range known {
		if value == candidate {
			return value
		}
	}
	return ""
}

func knownClockFormats() []string { return asStrings(ClockFormats()) }
func knownDateFormats() []string  { return asStrings(DateFormats()) }
func knownNavPositions() []string { return asStrings(NavPositions()) }
func knownDayOverflows() []string { return asStrings(DayOverflows()) }

// asStrings flattens any of the presentation enumerations to their stored form.
//
// Generic over the four distinct named string types rather than four near
// identical loops, because the four lists are the single source of what the
// settings form offers and this is only asking them what they said.
func asStrings[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

// SettingsFor returns the instance's settings with the acting user's own
// presentation choices overlaid.
//
// One resolver, because there are two readers and they have to agree. The day
// pane is built in the service and the rest of the chrome is rendered from the
// page's settings in the web layer; when the overlay lived only in the web
// layer, a person who set their day to start at six got a six o'clock heading
// on a pane still drawn from eight, which is a worse answer than not offering
// the setting.
//
// Unset values - an empty string, a negative hour - mean the instance decides,
// which is what makes this invisible until somebody chooses. Callers that mean
// the instance's own settings, such as the screen that edits them, ask for
// Settings or SettingsForAdmin instead.
func (s *Service) SettingsFor(ctx context.Context) (Settings, error) {
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	actor, ok := auth.UserFrom(ctx)
	if !ok {
		return settings, nil
	}
	return personalise(settings, actor), nil
}

// personalise overlays one person's presentation choices on the instance's.
//
// The week's first day is deliberately absent: it decides which Monday a
// timesheet period is keyed by, so two people disagreeing about it would be two
// people disagreeing about which week was approved
// (docs/adr/0023-week-as-the-unit-of-approval.md).
func personalise(settings Settings, user domain.User) Settings {
	if user.ClockFormat != "" {
		settings.ClockFormat = user.ClockFormat
	}
	if user.DateFormat != "" {
		settings.DateFormat = user.DateFormat
	}
	if user.NavPosition != "" {
		settings.NavPosition = user.NavPosition
	}
	if user.DayOverflow != "" {
		settings.DayOverflow = user.DayOverflow
	}
	// The hours travel as a pair. Taking one of somebody's and one of the
	// instance's could produce an end before a start, which the normaliser
	// would then silently swap: a sensible thing to do with a typo and a
	// confusing thing to do with a preference.
	// Part time is a property of the person, not the organisation, so this one
	// is here with the presentation rather than on the settings screen - but
	// it is not presentation: it changes what the week view says about whether
	// somebody is short, which is why zero means "do not compare at all"
	// rather than "a week of no hours".
	// Strictly positive, for the reason expectedMinutesFor explains: a
	// domain.User built in Go carries zero, and treating that as a deliberate
	// "no target" turns the comparison off for everybody who was never read
	// out of the database.
	if user.ExpectedWeeklyMinutes > 0 {
		settings.ExpectedWeeklyMinutes = user.ExpectedWeeklyMinutes
	}
	if user.DayStartHour >= 0 && user.DayEndHour >= 0 {
		window := domain.NormaliseDayWindow(user.DayStartHour, user.DayEndHour)
		settings.DayStartHour, settings.DayEndHour = window.StartHour, window.EndHour
	}
	return settings
}
