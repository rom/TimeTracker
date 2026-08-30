package store

import (
	"context"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Timesheet periods, and the week total an approval is a decision about.
//
// The interesting part is not the CRUD, it is the two rules underneath it: a
// missing row means the week is open rather than missing, and the week total
// counts only what counts. Both are the kind of rule that is right until
// somebody writes a second query.

// TestAnUnrecordedWeekIsOpen.
//
// Most weeks have no row at all - a period is only written when somebody submits
// one - so "not found" is the normal state and returning an error for it would
// make every screen handle the ordinary case as a failure.
func TestAnUnrecordedWeekIsOpen(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	period, err := db.GetPeriod(ctx, user.ID, "2026-03-16")
	if err != nil {
		t.Fatalf("get an unrecorded week: %v", err)
	}
	if period.Status != domain.PeriodOpen {
		t.Errorf("an unrecorded week has status %q, want open", period.Status)
	}
	if period.UserID != user.ID || period.WeekStart != "2026-03-16" {
		t.Errorf("the returned period is not about the week that was asked for: %+v", period)
	}
	if period.ID != 0 {
		t.Errorf("an unrecorded week came back with id %d", period.ID)
	}
}

// TestSubmittingTwiceUpdatesOneWeek.
//
// Two tabs, or a double-click, or a resubmission after a rejection. The unique
// index on (user, week) is what makes it one row; without it a week would have
// two records with different statuses and the approval screen would show both.
func TestSubmittingTwiceUpdatesOneWeek(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	submitted := time.Date(2026, 3, 20, 17, 0, 0, 0, time.UTC)
	period := domain.TimesheetPeriod{
		UserID: user.ID, WeekStart: "2026-03-16", Status: domain.PeriodSubmitted,
		SubmittedAt: submitted, SubmittedSeconds: 144000,
	}
	if err := db.UpsertPeriod(ctx, period); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Rejected, then resubmitted with a corrected total.
	period.Status = domain.PeriodRejected
	period.Note = "Thursday is missing"
	if err := db.UpsertPeriod(ctx, period); err != nil {
		t.Fatalf("reject: %v", err)
	}
	period.Status = domain.PeriodSubmitted
	period.SubmittedSeconds = 151200
	period.Note = ""
	if err := db.UpsertPeriod(ctx, period); err != nil {
		t.Fatalf("resubmit: %v", err)
	}

	weeks, err := db.ListPeriodsForUser(ctx, user.ID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(weeks) != 1 {
		t.Fatalf("one week submitted three times produced %d rows", len(weeks))
	}
	if weeks[0].Status != domain.PeriodSubmitted {
		t.Errorf("status = %q after resubmission", weeks[0].Status)
	}
	if weeks[0].SubmittedSeconds != 151200 {
		t.Errorf("submitted seconds = %d, want the corrected total",
			weeks[0].SubmittedSeconds)
	}
	if weeks[0].Note != "" {
		t.Errorf("the rejection note survived the resubmission: %q", weeks[0].Note)
	}
	if weeks[0].UserName == "" {
		t.Error("the period came back without the person's name; the approval " +
			"screen is a list of names")
	}
}

// TestSubmittedSecondsAreWhatWasSubmitted.
//
// An approval is a decision about specific figures. If the total were recomputed
// when the screen renders, an approver would be agreeing to whatever the week
// says at the moment they click - which is not what they read.
func TestSubmittedSecondsAreWhatWasSubmitted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
		UserID: user.ID, WeekStart: "2026-03-16", Status: domain.PeriodSubmitted,
		SubmittedAt:      time.Date(2026, 3, 20, 17, 0, 0, 0, time.UTC),
		SubmittedSeconds: 3600,
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Time is added afterwards, as it would be by somebody who forgot a day.
	start := time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	if _, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: start, EndedAt: &end, DurationSeconds: 7200,
		Status: domain.StatusConfirmed, TimeZone: "UTC",
	}); err != nil {
		t.Fatalf("create entry: %v", err)
	}

	period, err := db.GetPeriod(ctx, user.ID, "2026-03-16")
	if err != nil {
		t.Fatalf("get period: %v", err)
	}
	if period.SubmittedSeconds != 3600 {
		t.Errorf("submitted seconds = %d; the figure recorded at submission must "+
			"not move when the week does", period.SubmittedSeconds)
	}
}

// TestTheApprovalQueueIsByStatus.
//
// It is the screen a manager works from, so it has to contain every submitted
// week and nothing else - an approved week that stayed in the queue is one
// somebody approves twice, and a submitted week that fell out is one that never
// gets paid.
func TestTheApprovalQueueIsByStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	for _, week := range []struct {
		start  string
		status domain.PeriodStatus
	}{
		{"2026-03-02", domain.PeriodApproved},
		{"2026-03-09", domain.PeriodSubmitted},
		{"2026-03-16", domain.PeriodSubmitted},
		{"2026-03-23", domain.PeriodRejected},
	} {
		if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
			UserID: user.ID, WeekStart: week.start, Status: week.status,
			SubmittedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("upsert %s: %v", week.start, err)
		}
	}

	queue, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted,
		Scope{Unrestricted: true})
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("the queue holds %d weeks, want the 2 submitted ones", len(queue))
	}
	for _, period := range queue {
		if period.Status != domain.PeriodSubmitted {
			t.Errorf("a %s week is in the approval queue: %+v", period.Status, period)
		}
	}
}

// TestWeekSecondsCountsTheWeekAndOnlyTheWeek.
//
// The boundaries are the whole test. The bounds are half-open and compared
// against the stored timestamp directly - which is what makes the query use an
// index rather than calling date() on every row a person has ever recorded - so
// this checks the two instants that decide whether that comparison is right: the
// last second of the previous week and the first of the next.
func TestWeekSecondsCountsTheWeekAndOnlyTheWeek(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	record := func(at time.Time, seconds int64, status domain.EntryStatus, flagged bool) {
		t.Helper()
		end := at.Add(time.Duration(seconds) * time.Second)
		if _, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt: at, EndedAt: &end, DurationSeconds: seconds,
			Status: status, Flagged: flagged, TimeZone: "UTC",
		}); err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}

	record(monday.Add(-time.Second), 3600, domain.StatusConfirmed, false)   // the week before
	record(monday.Add(9*time.Hour), 3600, domain.StatusConfirmed, false)    // Monday
	record(monday.Add(6*24*time.Hour), 1800, domain.StatusConfirmed, false) // Sunday
	record(monday.Add(7*24*time.Hour), 3600, domain.StatusConfirmed, false) // the next Monday
	record(monday.Add(10*time.Hour), 7200, domain.StatusPending, false)     // a proposal
	record(monday.Add(11*time.Hour), 7200, domain.StatusConfirmed, true)    // flagged

	seconds, err := db.WeekSeconds(ctx, user.ID, "2026-03-16")
	if err != nil {
		t.Fatalf("week seconds: %v", err)
	}
	if seconds != 5400 {
		t.Errorf("week total = %d, want 5400 (one hour on Monday and half an hour "+
			"on Sunday; the neighbouring weeks, the proposal and the flagged entry "+
			"are all excluded)", seconds)
	}
}

// TestWeekSecondsIsPerPerson.
//
// The week banner appears on every screen where time is entered, so a total that
// leaked somebody else's hours would be visible constantly and believed.
func TestWeekSecondsIsPerPerson(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	other := seedUser(t, db, "Somebody Else")

	monday := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	for _, owner := range []int64{user.ID, other.ID} {
		end := monday.Add(time.Hour)
		if _, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: owner, EnteredBy: owner, AssignmentID: assignment.ID,
			StartedAt: monday, EndedAt: &end, DurationSeconds: 3600,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
		}); err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}

	seconds, err := db.WeekSeconds(ctx, user.ID, "2026-03-16")
	if err != nil {
		t.Fatalf("week seconds: %v", err)
	}
	if seconds != 3600 {
		t.Errorf("week total = %d, want only this person's 3600", seconds)
	}
}

// TestAnEmptyWeekTotalsZero.
//
// SUM over no rows is NULL in SQL, not zero. Scanning that into an int64 without
// a NullInt64 in between is an error at the point where a person's first ever
// screen renders - which is the worst first impression available.
func TestAnEmptyWeekTotalsZero(t *testing.T) {
	db := newTestDB(t)
	user, _ := seed(t, db)

	seconds, err := db.WeekSeconds(context.Background(), user.ID, "2026-03-16")
	if err != nil {
		t.Fatalf("a week with nothing in it: %v", err)
	}
	if seconds != 0 {
		t.Errorf("an empty week totalled %d", seconds)
	}
}

// TestAnApproverWithNoProjectsSeesNothing.
//
// The scope fails closed. A manager whose memberships have not been set up yet
// has an empty project list, and an empty list must mean "nothing" rather than
// the absence of a restriction - which is the shape of mistake that turns a
// scoped query into an unscoped one and shows one manager the whole company's
// timesheets.
func TestAnApproverWithNoProjectsSeesNothing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
		UserID: user.ID, WeekStart: "2026-03-16", Status: domain.PeriodSubmitted,
		SubmittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	queue, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted, Scope{})
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(queue) != 0 {
		t.Errorf("an approver with no projects and no customer saw %d weeks",
			len(queue))
	}
}
