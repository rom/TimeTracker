package service

import (
	"testing"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Presentation that follows the person rather than the instance.
//
// Theme, zone and language were always the account's. The hours the day pane
// covers, whether a clock reads 24-hour, how a date is written and where the
// navigation sits were the instance's, settable only by an administrator - so
// on a server with twenty people, one of them picked everybody's working hours
// and somebody who starts at six read their day in a pane drawn for nine.

// TestAnAccountWithNoPreferencesFollowsTheInstance.
//
// The property that makes the migration free. Every existing account has empty
// choices, and must see exactly what it saw before.
func TestAnAccountWithNoPreferencesFollowsTheInstance(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
		ClockFormat: "24h", DateFormat: "iso", NavPosition: "left",
		DayStartHour: 8, DayEndHour: 18, DayOverflow: "arrows",
	}); err != nil {
		t.Fatalf("save instance settings: %v", err)
	}

	resolved, err := f.svc.SettingsFor(f.ctx)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.ClockFormat != "24h" || resolved.DateFormat != "iso" ||
		resolved.NavPosition != "left" || resolved.DayOverflow != "arrows" ||
		resolved.DayStartHour != 8 || resolved.DayEndHour != 18 {
		t.Errorf("an account that has chosen nothing is not following the "+
			"instance: %+v", resolved)
	}
}

// TestAPersonsOwnChoicesWin.
func TestAPersonsOwnChoicesWin(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
		ClockFormat: "24h", DateFormat: "iso", NavPosition: "top",
		DayStartHour: 8, DayEndHour: 18, DayOverflow: "expand",
	}); err != nil {
		t.Fatalf("save instance settings: %v", err)
	}

	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		ClockFormat: "12h", DateFormat: "dmy", NavPosition: "left",
		DayOverflow: "arrows", DayStartHour: 6, DayEndHour: 14,
	}); err != nil {
		t.Fatalf("save preferences: %v", err)
	}

	// Read back through a context carrying the reloaded account, which is what
	// a request does: the overlay reads the user out of the context, so a
	// stale copy would silently show the instance's values.
	reloaded, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	mine := auth.WithUser(f.ctx, reloaded)

	resolved, err := f.svc.SettingsFor(mine)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.ClockFormat != "12h" || resolved.DateFormat != "dmy" ||
		resolved.NavPosition != "left" || resolved.DayOverflow != "arrows" ||
		resolved.DayStartHour != 6 || resolved.DayEndHour != 14 {
		t.Errorf("a person's own choices did not win: %+v", resolved)
	}

	// The instance's own settings are untouched, because the screen that edits
	// them has to show what everybody else gets rather than what this reader
	// happens to see.
	instance, err := f.svc.Settings(f.ctx)
	if err != nil {
		t.Fatalf("read the instance's: %v", err)
	}
	if instance.DayStartHour != 8 || instance.NavPosition != "top" {
		t.Errorf("one person's preferences changed the instance: %+v", instance)
	}
}

// TestTheDayPaneUsesTheReadersOwnWindow.
//
// The reason the overlay lives in the service rather than in the web layer.
// The pane is built here and the rest of the chrome is rendered there, and
// when only the web layer knew, somebody who set their day to start at six got
// a six o'clock heading above a pane still drawn from eight.
func TestTheDayPaneUsesTheReadersOwnWindow(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		DayStartHour: 6, DayEndHour: 14,
	}); err != nil {
		t.Fatalf("save preferences: %v", err)
	}
	reloaded, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	day, err := f.svc.Day(auth.WithUser(f.ctx, reloaded), f.now)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if day.StartHour != 6 || day.EndHour != 14 {
		t.Errorf("the day pane covers %02d:00-%02d:00, want the reader's own "+
			"06:00-14:00", day.StartHour, day.EndHour)
	}
}

// TestAnUnknownPreferenceMeansNoPreference.
//
// Unset is the meaningful default: it is how somebody says "follow the
// instance", so an edited form degrades to that rather than to some other
// arbitrary choice or to a mystery string in a class attribute.
func TestAnUnknownPreferenceMeansNoPreference(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		ClockFormat: "sundial", DateFormat: "stardate", NavPosition: "diagonal",
		DayOverflow: "explode", DayStartHour: 99, DayEndHour: -4,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	stored, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.ClockFormat != "" || stored.DateFormat != "" ||
		stored.NavPosition != "" || stored.DayOverflow != "" {
		t.Errorf("an unrecognised choice was stored: %+v", stored)
	}
	if stored.DayStartHour != -1 || stored.DayEndHour != -1 {
		t.Errorf("an impossible window was stored as %d-%d, want no preference",
			stored.DayStartHour, stored.DayEndHour)
	}
}

// TestHalfAWindowIsNoWindow.
//
// Taking one of somebody's hours and one of the instance's could produce an end
// before a start, which the normaliser would then silently swap - sensible for
// a typo, confusing for a preference.
func TestHalfAWindowIsNoWindow(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		DayStartHour: 6, DayEndHour: -1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	stored, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.DayStartHour != -1 || stored.DayEndHour != -1 {
		t.Errorf("half a window was stored as %d-%d; a window is two numbers "+
			"or none", stored.DayStartHour, stored.DayEndHour)
	}
}

// TestTheWeeksFirstDayStaysTheInstances.
//
// Not an oversight. It decides which Monday a timesheet period is keyed by, so
// two people disagreeing about it would be two people disagreeing about which
// week was approved (ADR-0023). If it is ever made personal, this test is the
// thing that should have to be deleted deliberately.
func TestTheWeeksFirstDayStaysTheInstances(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 7,
	}); err != nil {
		t.Fatalf("save instance settings: %v", err)
	}
	// Whatever a person has chosen, there is nowhere for a week start to be
	// chosen: the field does not exist on the account.
	resolved, err := f.svc.SettingsFor(auth.WithUser(f.ctx, domain.User{
		ID: f.user.ID, ClockFormat: "12h", DayStartHour: -1, DayEndHour: -1,
	}))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.WeekStart != 7 {
		t.Errorf("the week starts on %d for this reader, want the instance's 7",
			resolved.WeekStart)
	}
}

// TestAWeekIsMeasuredAgainstAFullOne.
//
// A total with nothing to compare it against cannot answer the question a
// timesheet is opened to answer on a Thursday afternoon: am I on track. The
// week view had a total, the submission screen checked for empty days, and
// nothing knew what a full week was.
func TestAWeekIsMeasuredAgainstAFullOne(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record an hour: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	// The default is forty hours, which is a default rather than a claim.
	if view.ExpectedSeconds != 40*3600 {
		t.Errorf("a full week is %ds, want 40 hours", view.ExpectedSeconds)
	}
	if view.TotalSeconds != 3600 {
		t.Errorf("the week totals %ds, want one hour", view.TotalSeconds)
	}
}

// TestPartTimeIsAPropertyOfThePerson.
func TestPartTimeIsAPropertyOfThePerson(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdatePresentation(f.ctx, PresentationInput{
		DayStartHour: -1, DayEndHour: -1, ExpectedWeeklyMinutes: 1800, // 30h
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := f.db.GetUser(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	view, err := f.svc.Period(auth.WithUser(f.ctx, reloaded), f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.ExpectedSeconds != 30*3600 {
		t.Errorf("a full week is %ds for somebody who works thirty hours, "+
			"want 30 hours", view.ExpectedSeconds)
	}

	// And the instance is unchanged for everybody else.
	instance, err := f.svc.Settings(f.ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if instance.ExpectedWeeklyMinutes != 2400 {
		t.Errorf("one person's part-time week changed the instance's to %d",
			instance.ExpectedWeeklyMinutes)
	}
}

// TestAnInstanceCanDeclineToSetATarget.
//
// Zero is a deliberate choice, not an absent one: an instance that would
// rather not put a figure on somebody's week sets it once and the comparison
// disappears, leaving the bare total that was there before.
func TestAnInstanceCanDeclineToSetATarget(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
		ExpectedWeeklyMinutes: 0,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.ExpectedSeconds != 0 {
		t.Errorf("a full week is %ds after the instance turned the comparison "+
			"off", view.ExpectedSeconds)
	}
}
