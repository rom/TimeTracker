package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// The days somebody was not working.
//
// The feature is not really "a list of holidays" - it is the three things that
// stop happening once the application knows about them: a week off stops
// reading as a short week, the reminders stop nagging about it, and nobody has
// to invent a customer called "Internal" to book leave against.

// TestAWeekOffIsACompleteWeek.
//
// The whole point. Before this, a week's holiday read as thirty-eight hours
// short, which is the application telling somebody off for being on leave.
func TestAWeekOffIsACompleteWeek(t *testing.T) {
	f := newFixture(t)

	// The fixture's clock is a Monday. Monday to Friday away.
	monday := f.now
	friday := monday.AddDate(0, 0, 4)
	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceHoliday,
		StartsOn: monday.Format("2006-01-02"),
		EndsOn:   friday.Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("record a week off: %v", err)
	}

	view, err := f.svc.Period(f.ctx, monday)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.AbsentDays != 5 {
		t.Errorf("the week counts %d days away, want five", view.AbsentDays)
	}
	// Five working days of a five-working-day week: the whole of it.
	if view.AbsentSeconds != view.ExpectedSeconds {
		t.Errorf("five days away account for %ds of an expected %ds; a week "+
			"off is a complete week", view.AbsentSeconds, view.ExpectedSeconds)
	}
	if view.TotalSeconds+view.AbsentSeconds < view.ExpectedSeconds {
		t.Error("a week entirely on holiday still reads as short")
	}
}

// TestOneDayOffIsAFifthOfTheWeek.
func TestOneDayOffIsAFifthOfTheWeek(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceSick,
		StartsOn: f.now.Format("2006-01-02"),
		EndsOn:   f.now.Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("record a sick day: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.AbsentDays != 1 {
		t.Fatalf("one sick day counted as %d", view.AbsentDays)
	}
	if view.AbsentSeconds != view.ExpectedSeconds/5 {
		t.Errorf("a day away accounts for %ds of an expected %ds, want a fifth",
			view.AbsentSeconds, view.ExpectedSeconds)
	}
}

// TestAWeekendDayOffIsWorthNothing.
//
// Absence fills a share of the expected week, and the share is the week divided
// by its WORKING days. A holiday booked across a weekend should not make the
// week read as more than complete.
func TestAWeekendDayOffIsWorthNothing(t *testing.T) {
	f := newFixture(t)

	// The fixture's Monday, so the Saturday of the same week.
	saturday := f.now.AddDate(0, 0, 5)
	if !domain.IsWeekend(saturday) {
		t.Fatalf("%v is not a weekend; this test is not testing what it says", saturday)
	}
	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceHoliday,
		StartsOn: saturday.Format("2006-01-02"),
		EndsOn:   saturday.Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.AbsentDays != 0 || view.AbsentSeconds != 0 {
		t.Errorf("a Saturday off counted as %d days and %ds; the share is of "+
			"the working week", view.AbsentDays, view.AbsentSeconds)
	}
}

// TestTheRemindersLeaveADayAwayAlone.
//
// The reason a timesheet that cannot record leave is worse than one with no
// reminders: it nags people for being on holiday.
func TestTheRemindersLeaveADayAwayAlone(t *testing.T) {
	f := newFixture(t)

	// Late enough in the week that the empty-day nudge has something to say.
	f.now = f.now.AddDate(0, 0, 2).Add(17 * time.Hour)
	weekStart := f.now.AddDate(0, 0, -2).Truncate(24 * time.Hour)

	before, err := f.svc.emptyWeekdays(f.ctx, f.user, weekStart, time.UTC)
	if err != nil {
		t.Fatalf("empty weekdays: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("no empty days to begin with, so this test proves nothing")
	}

	// Away for the first of them.
	away := before[0]
	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceHoliday,
		StartsOn: away.Format("2006-01-02"),
		EndsOn:   away.Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	after, err := f.svc.emptyWeekdays(f.ctx, f.user, weekStart, time.UTC)
	if err != nil {
		t.Fatalf("empty weekdays: %v", err)
	}
	for _, day := range after {
		if day.Equal(away) {
			t.Errorf("%s is still reported as an empty day after being "+
				"recorded as leave", away.Format("2006-01-02"))
		}
	}
	if len(after) != len(before)-1 {
		t.Errorf("recording one day of leave changed the empty days from %d "+
			"to %d, want one fewer", len(before), len(after))
	}
}

// TestAPublicHolidayIsTheInstances.
//
// It applies to everybody, so recording one is administration - the same bound
// as a tag or the settings (ADR-0008) - and once recorded it counts for every
// person without any of them doing anything.
func TestAPublicHolidayIsTheInstances(t *testing.T) {
	f := newFixture(t)

	public, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsencePublic,
		StartsOn: f.now.Format("2006-01-02"),
		EndsOn:   f.now.Format("2006-01-02"),
		Note:     "a national holiday",
	})
	if err != nil {
		t.Fatalf("record a public holiday as the administrator: %v", err)
	}
	if public.UserID != 0 {
		t.Errorf("a public holiday was stored against user %d; it is the "+
			"instance's, not whoever typed it in", public.UserID)
	}

	// Somebody else sees it without having recorded anything.
	colleague, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create colleague: %v", err)
	}
	theirs := auth.WithUser(f.ctx, colleague)

	absences, err := f.svc.Absences(theirs, f.now, f.now)
	if err != nil {
		t.Fatalf("list as the colleague: %v", err)
	}
	var found bool
	for _, absence := range absences {
		if absence.ID == public.ID {
			found = true
		}
	}
	if !found {
		t.Error("a public holiday does not apply to everybody")
	}

	// And it counts towards their week too.
	view, err := f.svc.Period(theirs, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.AbsentDays != 1 {
		t.Errorf("a public holiday counted as %d days away for somebody who "+
			"did not record it", view.AbsentDays)
	}
}

// TestRecordingAnAbsenceIsNotBlockedByAnApprovedWeek.
//
// Deliberate. An absence records that somebody was away, which is a fact about
// the past rather than a figure in the timesheet: it adds no hours and changes
// no approved total. Refusing last month's sick day because the week has been
// signed off would leave the record permanently wrong.
func TestRecordingAnAbsenceIsNotBlockedByAnApprovedWeek(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if _, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceSick,
		StartsOn: f.now.Format("2006-01-02"),
		EndsOn:   f.now.Format("2006-01-02"),
	}); err != nil {
		t.Errorf("a sick day could not be recorded against a submitted week, "+
			"which leaves the record permanently wrong: %v", err)
	}
}

// TestAnAbsenceIsCheckedBeforeItIsStored.
func TestAnAbsenceIsCheckedBeforeItIsStored(t *testing.T) {
	f := newFixture(t)

	for _, attempt := range []struct {
		name string
		in   AbsenceInput
	}{
		{"an unknown kind", AbsenceInput{Kind: "sabbatical", StartsOn: "2026-03-16", EndsOn: "2026-03-16"}},
		{"ending before it begins", AbsenceInput{Kind: domain.AbsenceHoliday, StartsOn: "2026-03-20", EndsOn: "2026-03-16"}},
		{"a date that is not one", AbsenceInput{Kind: domain.AbsenceHoliday, StartsOn: "next tuesday", EndsOn: "2026-03-16"}},
		{"longer than a year", AbsenceInput{Kind: domain.AbsenceHoliday, StartsOn: "2026-01-01", EndsOn: "2028-01-01"}},
	} {
		if _, err := f.svc.RecordAbsence(f.ctx, attempt.in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s was accepted: %v", attempt.name, err)
		}
	}
}

// TestDeletingAnAbsenceIsRecoverable.
func TestDeletingAnAbsenceIsRecoverable(t *testing.T) {
	f := newFixture(t)

	absence, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceHoliday,
		StartsOn: f.now.Format("2006-01-02"),
		EndsOn:   f.now.Format("2006-01-02"),
		Note:     "a day off",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := f.svc.DeleteAbsence(f.ctx, absence.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	bin, err := f.svc.RecentlyDeleted(f.ctx)
	if err != nil || len(bin) != 1 {
		t.Fatalf("bin = %d records, err %v", len(bin), err)
	}
	if !strings.Contains(bin[0].Summary, "a day off") {
		t.Errorf("the bin entry does not say what was deleted: %q", bin[0].Summary)
	}
	if err := f.svc.RestoreDeleted(f.ctx, bin[0].ID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	view, err := f.svc.Period(f.ctx, f.now)
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if view.AbsentDays != 1 {
		t.Error("a restored absence does not count towards the week again")
	}
}

// TestAColleaguesAbsenceIsNotEverybodysToRemove.
func TestAColleaguesAbsenceIsNotEverybodysToRemove(t *testing.T) {
	f := newFixture(t)

	absence, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceHoliday,
		StartsOn: f.now.Format("2006-01-02"),
		EndsOn:   f.now.Format("2006-01-02"),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	stranger, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Stranger", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	// A member with no claim on this person, through an authorizer that
	// actually enforces membership rather than the fixture's single-user one.
	office, _ := officeContext(t, f)
	if err := office.DeleteAbsence(auth.WithUser(f.ctx, stranger), absence.ID); err == nil {
		t.Error("somebody else's holiday was removed by a colleague with no claim on it")
	}
}

// Correcting one that is already recorded.
//
// The case this exists for is a sick day that turned into three. Until there
// was an edit, saying so meant deleting the record and typing it again: two
// actions for one correction, and the original gone.
func TestAnAbsenceCanBeCorrected(t *testing.T) {
	f := newFixture(t)

	absence, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceSick,
		StartsOn: "2026-03-16",
		EndsOn:   "2026-03-16",
		Note:     "Flu",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	updated, err := f.svc.UpdateAbsence(f.ctx, absence.ID, AbsenceInput{
		Kind:     domain.AbsenceSick,
		StartsOn: "2026-03-16",
		EndsOn:   "2026-03-18",
		Note:     "Flu, and it kept going",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.EndsOn != "2026-03-18" {
		t.Errorf("ends on %q, want 2026-03-18", updated.EndsOn)
	}

	// And it is one record afterwards, not two.
	stored, err := f.svc.Absences(f.ctx,
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("%d absences recorded, want 1: a correction is not a second record", len(stored))
	}
	if stored[0].Note != "Flu, and it kept going" {
		t.Errorf("note is %q, so the correction did not land", stored[0].Note)
	}

	// The whole point: the longer absence now covers the extra days.
	days, err := f.svc.absentDaysFor(f.ctx, f.user.ID,
		time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("absent days: %v", err)
	}
	for _, day := range []string{"2026-03-16", "2026-03-17", "2026-03-18"} {
		if _, away := days[day]; !away {
			t.Errorf("%s is not covered after the correction", day)
		}
	}
}

// A correction may not move a record across the line between a person's own
// leave and the instance's day off. The two answer to different permissions,
// so a form that could do it quietly would be a way around the one that
// refuses.
func TestACorrectionCannotTurnLeaveIntoAPublicHoliday(t *testing.T) {
	f := newFixture(t)

	mine, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind: domain.AbsenceHoliday, StartsOn: "2026-03-16", EndsOn: "2026-03-16",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	_, err = f.svc.UpdateAbsence(f.ctx, mine.ID, AbsenceInput{
		Kind: domain.AbsencePublic, StartsOn: "2026-03-16", EndsOn: "2026-03-16",
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("turning a holiday into everybody's day off returned %v, "+
			"want a refusal: the two are different records", err)
	}
}

// Changing somebody else's leave is the same power as deleting it, and is
// bounded the same way. This is the defect that was found in DeleteAbsence
// written again for the edit, because an edit that was not bounded would be
// the same hole with a different verb.
func TestAColleaguesAbsenceIsNotEverybodysToCorrect(t *testing.T) {
	f := newFixture(t)

	absence, err := f.svc.RecordAbsence(f.ctx, AbsenceInput{
		Kind:     domain.AbsenceHoliday,
		StartsOn: f.now.Format("2006-01-02"),
		EndsOn:   f.now.Format("2006-01-02"),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	stranger, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Stranger", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}

	office, _ := officeContext(t, f)
	if _, err := office.UpdateAbsence(auth.WithUser(f.ctx, stranger), absence.ID,
		AbsenceInput{
			Kind:     domain.AbsenceHoliday,
			StartsOn: f.now.Format("2006-01-02"),
			EndsOn:   f.now.AddDate(0, 0, 14).Format("2006-01-02"),
		}); err == nil {
		t.Error("a colleague with no claim on this person extended their holiday")
	}
}
