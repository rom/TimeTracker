package service

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Instance-wide settings, administered rather than recorded.
//
// These are deliberately few. Every one of them is a decision that genuinely
// differs between organisations - a billing increment, which day the week starts
// on - rather than a preference the designer declined to make.

// Settings is the instance-wide configuration, re-exported so the HTTP layer
// can name it without importing the store package
// (docs/adr/0012-layered-package-structure.md).
type Settings = store.Settings

// SettingsInput is what the administration form supplies.
type SettingsInput struct {
	DefaultCurrency string
	DefaultRounding string
	DefaultRate     string
	WeekStart       int
	MaxTimerHours   int64
	// IdleMinutes is how long a stretch the application saw nothing has to be
	// before the page reports it; zero or absent leaves it as it was.
	// IdleDisabled is the switch that turns the whole thing off, and is
	// separate for the same reason ClearBackupPassword is: a zero that means
	// both "unchanged" and "off" would let any caller who forgot the field
	// silently disable a feature.
	IdleMinutes  int64
	IdleDisabled bool
	// ReminderHour is the hour of the local day from which end-of-day nudges
	// appear; outside 1-23 it is left as it was. RemindersDisabled is the
	// switch, separate for the same reason IdleDisabled is.
	ReminderHour      int
	RemindersDisabled bool
	ShowClock         bool
	ShowTimeAndDate   bool

	// Presentation.
	NavPosition  string
	ClockFormat  string
	DateFormat   string
	DayStartHour int
	DayEndHour   int
	DayOverflow  string

	// WeekendRecordingDisabled refuses time on a Saturday or a Sunday. Phrased
	// as the refusal rather than the permission, like RemindersDisabled above
	// and for the same reason: a caller that leaves the field out gets the
	// permissive value. Spelt the other way round, every existing caller that
	// does not know about this field - and there are several, in tests and in
	// the importers' settings - would quietly start refusing weekends.
	// ExpectedWeeklyMinutes is a full working week; negative leaves it as it
	// was, and zero turns the comparison off.
	ExpectedWeeklyMinutes    int
	WeekendRecordingDisabled bool
	// WeekendColour is a palette key for the day numbers of a weekend.
	WeekendColour string

	// BackupPassword is write-only from the form's point of view: it is never
	// rendered back, so an empty field means "leave it as it is" rather than
	// "clear it". ClearBackupPassword is how it is actually removed, because
	// otherwise there would be no way to turn encryption off again.
	BackupPassword      string
	ClearBackupPassword bool
}

// UpdateSettings saves the instance-wide settings.
func (s *Service) UpdateSettings(ctx context.Context, in SettingsInput) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "settings"}); err != nil {
		return err
	}

	existing, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}

	currency := existing.DefaultCurrency
	if in.DefaultCurrency != "" {
		if len(in.DefaultCurrency) != 3 {
			return fmt.Errorf("%w: a currency is a three-letter ISO-4217 code", ErrValidation)
		}
		currency = in.DefaultCurrency
	}

	// An unrecognised rounding rule would silently degrade to "no rounding" when
	// read back, which is a quiet way to under-bill; refuse it instead.
	rounding := in.DefaultRounding
	if rounding == "" {
		rounding = "none"
	}
	if !knownRoundingRule(rounding) {
		return fmt.Errorf("%w: unknown rounding rule %q", ErrValidation, rounding)
	}

	rate, err := domain.ParseMoney(in.DefaultRate, currency)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err)
	}
	if rate.Minor < 0 {
		return fmt.Errorf("%w: a rate cannot be negative", ErrValidation)
	}

	weekStart := in.WeekStart
	if weekStart < 1 || weekStart > 7 {
		weekStart = existing.WeekStart
	}

	maxTimer := in.MaxTimerHours * 3600
	if maxTimer <= 0 {
		maxTimer = existing.MaxTimerSeconds
	}
	if maxTimer > 7*24*3600 {
		return fmt.Errorf("%w: a timer limit above a week defeats the purpose of the check",
			ErrValidation)
	}

	idle := existing.IdleSeconds
	switch {
	case in.IdleDisabled:
		idle = 0
	case in.IdleMinutes > 0:
		idle = in.IdleMinutes * 60
	}
	if idle > 24*3600 {
		return fmt.Errorf("%w: an idle threshold above a day would never be reached",
			ErrValidation)
	}

	reminderHour := existing.ReminderHour
	if in.ReminderHour > 0 && in.ReminderHour < 24 {
		reminderHour = in.ReminderHour
	}

	// Presentation choices degrade to a known value rather than being refused.
	// A rate that will not parse is worth stopping the form for; a navigation
	// position nobody recognises is not worth making somebody re-enter their
	// currency over, and leaving it unvalidated would put a mystery string in
	// a class attribute.
	window := domain.NormaliseDayWindow(in.DayStartHour, in.DayEndHour)

	// The same reasoning for the weekend's colour: a key the stylesheet has no
	// rule for would render as no colour at all, which is a setting that looks
	// saved and does nothing. Falling back to the previous value keeps the
	// screen honest about what is in force.
	// Negative means the form did not say, which leaves the instance's figure
	// alone; zero is a deliberate "do not compare" and is kept.
	expectedWeek := existing.ExpectedWeeklyMinutes
	if in.ExpectedWeeklyMinutes >= 0 && in.ExpectedWeeklyMinutes <= 7*24*60 {
		expectedWeek = in.ExpectedWeeklyMinutes
	}

	weekendColour := domain.ColourKeyOr(in.WeekendColour,
		domain.ColourKeyOr(existing.WeekendColour, "red"))

	// The backup password: absent means unchanged, because the form never shows
	// it back and an empty field would otherwise silently disable encryption
	// the first time somebody saved an unrelated setting.
	backupPassword := existing.BackupPassword
	switch {
	case in.ClearBackupPassword:
		backupPassword = ""
	case in.BackupPassword != "":
		backupPassword = in.BackupPassword
	}

	updated := store.Settings{
		DefaultCurrency:       currency,
		DefaultRounding:       rounding,
		DefaultRateMinor:      rate.Minor,
		WeekStart:             weekStart,
		MaxTimerSeconds:       maxTimer,
		IdleSeconds:           idle,
		RemindersEnabled:      !in.RemindersDisabled,
		ReminderHour:          reminderHour,
		ShowClock:             in.ShowClock,
		ShowTimeAndDate:       in.ShowTimeAndDate,
		NavPosition:           string(domain.NavPosition(in.NavPosition).OrDefault()),
		ClockFormat:           string(domain.ClockFormat(in.ClockFormat).OrDefault()),
		DateFormat:            string(domain.DateFormat(in.DateFormat).OrDefault()),
		DayStartHour:          window.StartHour,
		DayEndHour:            window.EndHour,
		DayOverflow:           string(domain.DayOverflow(in.DayOverflow).OrDefault()),
		ExpectedWeeklyMinutes: expectedWeek,
		WeekendRecording:      !in.WeekendRecordingDisabled,
		WeekendColour:         weekendColour,
		BackupPassword:        backupPassword,
	}
	// Audited: a rounding or rate default changes what future work is worth, so
	// a later question about a figure needs to find when it changed. The entry
	// records *that* the backup password changed, never what it became - an
	// audit log is read by more people than the settings form is.
	return s.mutate(ctx, "settings.update", "settings", map[string]any{
		"default_currency": map[string]any{"from": existing.DefaultCurrency, "to": currency},
		"default_rounding": map[string]any{"from": existing.DefaultRounding, "to": rounding},
		"default_rate":     map[string]any{"from": existing.DefaultRateMinor, "to": rate.Minor},
		"week_start":       map[string]any{"from": existing.WeekStart, "to": weekStart},
		// Audited because it is a policy rather than a preference: turning
		// weekend recording off changes what the application will accept, and
		// somebody whose Saturday entry was refused needs the trail to say
		// when that became true and who decided it.
		"weekend_recording": map[string]any{
			"from": existing.WeekendRecording, "to": !in.WeekendRecordingDisabled,
		},
		"backup_encrypted": map[string]any{
			"from": existing.BackupPassword != "", "to": backupPassword != "",
		},
	}, func(tx *sql.Tx) (int64, error) {
		return 1, store.UpdateSettingsTx(ctx, tx, updated)
	})
}

// knownRoundingRule reports whether a stored value is one of the presets.
//
// Checking against the preset list rather than merely parsing means a typo is
// caught at the point of entry, instead of degrading silently to "no rounding"
// the next time an entry is billed.
func knownRoundingRule(key string) bool {
	for _, preset := range domain.NamedRoundingRules {
		if preset.Key == key {
			return true
		}
	}
	return false
}

// RoundingPresets returns the selectable rounding rules, for the form.
func RoundingPresets() []struct{ Key, MessageKey string } {
	presets := make([]struct{ Key, MessageKey string }, 0, len(domain.NamedRoundingRules))
	for _, preset := range domain.NamedRoundingRules {
		presets = append(presets, struct{ Key, MessageKey string }{preset.Key, preset.MessageKey})
	}
	return presets
}

// The choices the settings form offers. Re-exported from the domain so the
// template ranges over the same lists the validator accepts, and so the web
// layer does not have to reach past the service to reach them
// (docs/adr/0012-layered-package-structure.md).

// NavPositions are top and left.
func NavPositions() []domain.NavPosition { return domain.NavPositions }

// ClockFormats are automatic, 24-hour and 12-hour.
func ClockFormats() []domain.ClockFormat { return domain.ClockFormats }

// DateFormats are automatic, ISO, day-first and month-first.
func DateFormats() []domain.DateFormat { return domain.DateFormats }

// DayOverflows are expand and arrows.
func DayOverflows() []domain.DayOverflow { return domain.DayOverflows }
