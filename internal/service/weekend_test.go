package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The weekend policy.
//
// Two halves, and the second is the one worth testing hardest: refusing new
// time on a Saturday is easy, and leaving every Saturday already recorded
// workable is what stops the setting from being a trap. An instance that turned
// the policy on and thereby froze last March is an instance that cannot turn it
// on.

// forbidWeekends turns weekend recording off, through the real settings path.
func (f *fixture) forbidWeekends(t *testing.T) {
	t.Helper()

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
		WeekendRecordingDisabled: true,
	}); err != nil {
		t.Fatalf("forbid weekends: %v", err)
	}
}

// saturday and monday are days in the fixture's week. The fixture's clock sits
// on Monday 2026-03-16, so these are that week's days.
var (
	saturday = time.Date(2026, 3, 21, 9, 0, 0, 0, time.UTC)
	sunday   = time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC)
	tuesday  = time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC)
)

// TestWeekendsAreRecordableUntilAnAdministratorSaysOtherwise.
//
// The default has to be permissive. The column was added to a database with
// years of weekends already in it, and a migration that silently started
// refusing them would be a migration that loses somebody's Saturday.
func TestWeekendsAreRecordableUntilAnAdministratorSaysOtherwise(t *testing.T) {
	f := newFixture(t)

	settings, err := f.db.GetSettings(f.ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !settings.WeekendRecording {
		t.Error("a fresh instance refuses weekends; the default has to be the " +
			"behaviour that existed before the setting did")
	}

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: saturday,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Errorf("a Saturday was refused with the policy off: %v", err)
	}
}

// TestASettingsSaveThatSaysNothingAboutWeekendsLeavesThemAlone.
//
// SettingsInput is filled in by hand in a dozen places. If the field were
// phrased as the permission rather than the refusal, every one of those callers
// would turn the policy on by omission - which is how a feature nobody asked
// for starts refusing people's time.
func TestASettingsSaveThatSaysNothingAboutWeekendsLeavesThemAlone(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	settings, err := f.db.GetSettings(f.ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !settings.WeekendRecording {
		t.Error("saving unrelated settings forbade weekends")
	}
}

// TestNewTimeIsRefusedOnAWeekendWhenTheInstanceForbidsIt.
func TestNewTimeIsRefusedOnAWeekendWhenTheInstanceForbidsIt(t *testing.T) {
	f := newFixture(t)
	f.forbidWeekends(t)

	for _, day := range []struct {
		name string
		when time.Time
	}{{"Saturday", saturday}, {"Sunday", sunday}} {
		_, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID, StartedAt: day.when,
			DurationSeconds: 3600, Billable: true,
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s was accepted: %v", day.name, err)
			continue
		}
		// The refusal names the day. Somebody who believed they picked a
		// working day needs to be told which day they actually picked.
		if !strings.Contains(err.Error(), day.when.Format("Monday 2 January 2006")) {
			t.Errorf("the refusal for %s does not name the day: %v", day.name, err)
		}
	}

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: tuesday,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Errorf("a Tuesday was refused: %v", err)
	}
}

// TestATimerCannotBeStartedOnAForbiddenWeekend.
//
// At Start rather than at Stop. Refusing when the timer stops would be refusing
// after the work is done, with an hour recorded and nowhere to put it.
func TestATimerCannotBeStartedOnAForbiddenWeekend(t *testing.T) {
	f := newFixture(t)
	f.forbidWeekends(t)
	f.now = saturday

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "weekend work"); !errors.Is(err, ErrValidation) {
		t.Errorf("a timer started on a Saturday: %v", err)
	}
}

// TestATimerRunningIntoAForbiddenWeekendCanStillBeStopped.
//
// The policy governs where time arrives, not what happens to time already
// running. A timer started on Friday afternoon and stopped on Saturday morning
// has to stop: refusing would leave it running for ever, which is the one
// outcome worse than an hour on a Saturday.
func TestATimerRunningIntoAForbiddenWeekendCanStillBeStopped(t *testing.T) {
	f := newFixture(t)

	// Friday, while weekends are still allowed.
	f.now = time.Date(2026, 3, 20, 22, 0, 0, 0, time.UTC)
	entry, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "late Friday")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	f.forbidWeekends(t)
	f.now = time.Date(2026, 3, 21, 1, 0, 0, 0, time.UTC) // Saturday

	stopped, err := f.svc.StopTimer(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("a running timer could not be stopped once weekends were "+
			"forbidden, which strands it for ever: %v", err)
	}
	if stopped.DurationSeconds != 3*3600 {
		t.Errorf("stopped duration = %ds, want 10800", stopped.DurationSeconds)
	}
}

// TestTimeAlreadyOnAWeekendStaysWorkable.
//
// The case that decides whether the setting can be turned on at all. An entry
// recorded on a Saturday before the policy existed must stay correctable,
// movable and deletable; only moving time ONTO a weekend is refused.
func TestTimeAlreadyOnAWeekendStaysWorkable(t *testing.T) {
	f := newFixture(t)

	existing, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: saturday,
		DurationSeconds: 3600, Note: "recorded while weekends were allowed",
		Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: sunday,
		DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	f.forbidWeekends(t)

	// Corrected in place: same day, different note. The entry is on a Saturday
	// and stays on it, so nothing has arrived anywhere new.
	if _, err := f.svc.UpdateEntry(f.ctx, existing.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: saturday,
		DurationSeconds: 7200, Note: "corrected", Billable: true,
	}); err != nil {
		t.Errorf("a weekend entry could not be corrected in place, which "+
			"freezes every Saturday already recorded: %v", err)
	}

	// Moved off the weekend: the thing the policy actually wants to happen.
	if _, err := f.svc.UpdateEntry(f.ctx, existing.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: tuesday,
		DurationSeconds: 7200, Note: "moved to a working day", Billable: true,
	}); err != nil {
		t.Errorf("a weekend entry could not be moved to a weekday: %v", err)
	}

	// Moved onto a weekend: refused, whichever direction it came from.
	if _, err := f.svc.UpdateEntry(f.ctx, existing.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: sunday,
		DurationSeconds: 7200, Billable: true,
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("an entry was moved onto a Sunday: %v", err)
	}

	// And deleting one is never refused: a policy that stranded the rows it
	// disapproves of would be worse than no policy.
	if err := f.svc.DeleteEntry(f.ctx, second.ID); err != nil {
		t.Errorf("a weekend entry could not be deleted: %v", err)
	}
}

// TestTheWeekendPolicyIsNotTheWeekLock.
//
// The two checks look alike and are called from the same functions, so it is
// worth stating what keeps them apart: the lock covers deleting and stopping,
// the weekend policy does not. If someone ever folds one into the other, this
// is the test that says which call sites would break.
func TestTheWeekendPolicyIsNotTheWeekLock(t *testing.T) {
	f := newFixture(t)

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: saturday,
		DurationSeconds: 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.forbidWeekends(t)

	// checkWeekendAllowed itself, on the paths that call it and the paths that
	// must not. Asserted at this level because the distinction is a decision
	// about call sites, and a test that only went through CreateEntry could
	// not tell a missing call from a permissive one.
	if err := f.svc.checkWeekendAllowed(f.ctx, f.user.ID, saturday); !errors.Is(err, ErrValidation) {
		t.Errorf("checkWeekendAllowed permitted a Saturday: %v", err)
	}
	if err := f.svc.checkWeekendAllowed(f.ctx, f.user.ID, tuesday); err != nil {
		t.Errorf("checkWeekendAllowed refused a Tuesday: %v", err)
	}
	// The lock still covers the Saturday's week, and says so in its own words.
	if err := f.svc.checkPeriodOpen(f.ctx, f.user.ID, saturday); err != nil {
		t.Errorf("checkPeriodOpen refused an open week: %v", err)
	}
	if err := f.svc.DeleteEntry(f.ctx, entry.ID); err != nil {
		t.Errorf("delete went through the weekend check: %v", err)
	}
}

// TestTheWeekendIsJudgedInTheOwnersZone.
//
// Which day an entry belongs to is answered in the owner's zone everywhere else
// in this application, and the weekend policy is not allowed to be the
// exception. 23:00 UTC on a Friday is already Saturday in Auckland.
func TestTheWeekendIsJudgedInTheOwnersZone(t *testing.T) {
	f := newFixture(t)
	f.forbidWeekends(t)

	owner, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Kiwi", Role: domain.RoleAdmin,
		TimeZone: "Pacific/Auckland", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// 23:00 UTC on Friday 20 March 2026 is 12:00 on Saturday the 21st in
	// Auckland. UTC says it is a working day; the person recording it is
	// looking at a Saturday.
	fridayInUTC := time.Date(2026, 3, 20, 23, 0, 0, 0, time.UTC)
	if domain.IsWeekend(fridayInUTC) {
		t.Fatal("the fixture instant is not the Friday this test needs")
	}
	if err := f.svc.checkWeekendAllowed(f.ctx, owner.ID, fridayInUTC); !errors.Is(err, ErrValidation) {
		t.Errorf("an instant that is Saturday for its owner was allowed "+
			"because it is Friday in UTC: %v", err)
	}
	// And the same instant for somebody in UTC is a Friday, and allowed.
	if err := f.svc.checkWeekendAllowed(f.ctx, f.user.ID, fridayInUTC); err != nil {
		t.Errorf("a Friday in the owner's own zone was refused: %v", err)
	}
}

// TestAnUnknownWeekendColourNeverReachesTheStore.
//
// The key is written into an attribute that the stylesheet matches on. A value
// with no matching rule is not a different colour - it is no colour at all, and
// a setting that saves cleanly and does nothing is worse than one that refuses.
func TestAnUnknownWeekendColourNeverReachesTheStore(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
		WeekendColour: "teal",
	}); err != nil {
		t.Fatalf("save teal: %v", err)
	}

	for _, attempt := range []string{"chartreuse", "#ff0000", "", "RED"} {
		if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
			DefaultCurrency: "SEK", DefaultRounding: "none", WeekStart: 1,
			WeekendColour: attempt,
		}); err != nil {
			t.Fatalf("save %q: %v", attempt, err)
		}
		settings, err := f.db.GetSettings(f.ctx)
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		if !domain.IsColourKey(settings.WeekendColour) {
			t.Errorf("saving %q stored %q, which names no palette entry",
				attempt, settings.WeekendColour)
		}
		if settings.WeekendColour != "teal" {
			t.Errorf("saving %q changed the colour to %q; an unrecognised "+
				"value should leave the previous one in force",
				attempt, settings.WeekendColour)
		}
	}
}
