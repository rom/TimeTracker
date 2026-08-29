package service

import (
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Idle observation.
//
// The feature reduces somebody's recorded hours, so the tests are written around
// what it must never do: record a stretch it was not told about, ask twice about
// one absence, act without being asked, or reach into a week that has been
// submitted.

// idleFixture makes a stopped entry from 09:00 to 15:00 and returns it.
func idleFixture(t *testing.T, f *fixture) domain.TimeEntry {
	t.Helper()
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID,
		StartedAt:    at(f.now, 9, 0),
		EndedAt:      timePtr(at(f.now, 15, 0)),
		Billable:     true, Note: "migration work",
	})
	if err != nil {
		t.Fatalf("record entry: %v", err)
	}
	return entry
}

func timePtr(t time.Time) *time.Time { return &t }

// TestIdleObservationBecomesAQuestion.
//
// The whole loop: a page reports what it saw, the person is asked about it with
// the consequences worked out, and answering changes the timesheet by exactly
// what the question said it would.
func TestIdleObservationBecomesAQuestion(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	recorded, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 12, 0), at(f.now, 13, 0), domain.IdleAsleep)
	if err != nil {
		t.Fatalf("record idle: %v", err)
	}
	if !recorded {
		t.Fatal("an hour inside the entry should have been recorded")
	}

	pending, err := f.svc.PendingIdle(f.ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d observations awaiting a decision, want 1", len(pending))
	}

	report := pending[0]
	if got := report.Observation.Seconds(); got != 3600 {
		t.Errorf("observed stretch is %ds, want 3600", got)
	}
	// The numbers the buttons will carry.
	if report.Discard.KeptSeconds != 3*3600 {
		t.Errorf("discard would keep %ds, want 3h", report.Discard.KeptSeconds)
	}
	if !report.CanSplit || report.Split.KeptSeconds != 5*3600 {
		t.Errorf("split would keep %ds (canSplit=%v), want 5h and true",
			report.Split.KeptSeconds, report.CanSplit)
	}

	if err := f.svc.ResolveIdle(f.ctx, report.Observation.ID, domain.IdleSplit); err != nil {
		t.Fatalf("split: %v", err)
	}

	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if len(day.Entries) != 2 {
		t.Fatalf("the day has %d entries after a split, want 2", len(day.Entries))
	}
	if day.Totals.SummedSeconds != 5*3600 {
		t.Errorf("the day totals %ds after removing an hour, want 5h",
			day.Totals.SummedSeconds)
	}
	// The second entry inherits what the first was, because it is the same work
	// on the other side of a break.
	for _, e := range day.Entries {
		if e.Note != "migration work" || e.AssignmentID != f.assignment.ID {
			t.Errorf("entry %d = %q on assignment %d, want the original's note and assignment",
				e.ID, e.Note, e.AssignmentID)
		}
	}

	// And the question is not asked again.
	if again, err := f.svc.PendingIdle(f.ctx); err != nil || len(again) != 0 {
		t.Errorf("after deciding, %d observations remain (err %v), want 0", len(again), err)
	}
}

// TestKeepingIdleTimeChangesNothingButTheQuestion.
//
// Keep is the default answer and has to be free of consequences: the hour stays
// on the timesheet, and the prompt does not come back.
func TestKeepingIdleTimeChangesNothingButTheQuestion(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	if _, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 12, 0), at(f.now, 13, 0), domain.IdleUntouched); err != nil {
		t.Fatalf("record idle: %v", err)
	}
	pending, _ := f.svc.PendingIdle(f.ctx)
	if len(pending) != 1 {
		t.Fatalf("%d pending, want 1", len(pending))
	}

	if err := f.svc.ResolveIdle(f.ctx, pending[0].Observation.ID, domain.IdleKeep); err != nil {
		t.Fatalf("keep: %v", err)
	}

	day, _ := f.svc.Day(f.ctx, f.now)
	if len(day.Entries) != 1 || day.Totals.SummedSeconds != 6*3600 {
		t.Errorf("after keeping: %d entries totalling %ds, want 1 and 6h",
			len(day.Entries), day.Totals.SummedSeconds)
	}
	if again, _ := f.svc.PendingIdle(f.ctx); len(again) != 0 {
		t.Errorf("keeping should settle the question; %d still pending", len(again))
	}
}

// TestIdleObservationsAreClampedAndDeduplicated.
//
// The times come from a browser. A stretch reaching outside the entry is fitted
// to it rather than believed, one that misses it entirely is dropped, and the
// same absence reported twice is one question rather than two - a laptop woken
// and slept again over lunch reports exactly that.
func TestIdleObservationsAreClampedAndDeduplicated(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	// Reaching an hour past both ends of the entry.
	if _, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 8, 0), at(f.now, 16, 0), domain.IdleAsleep); err != nil {
		t.Fatalf("record idle: %v", err)
	}
	pending, _ := f.svc.PendingIdle(f.ctx)
	if len(pending) != 1 {
		t.Fatalf("%d pending, want 1", len(pending))
	}
	o := pending[0].Observation
	if !o.StartedAt.Equal(at(f.now, 9, 0)) || !o.EndedAt.Equal(at(f.now, 15, 0)) {
		t.Errorf("stretch stored as %s-%s, want it clamped to the entry's 09:00-15:00",
			o.StartedAt.Format("15:04"), o.EndedAt.Format("15:04"))
	}
	// It now covers the whole entry, so there is nothing to keep either side of
	// it: the observation is still reported, with keep as the only answer.
	if pending[0].CanDiscard || pending[0].CanSplit {
		t.Error("a stretch covering the whole entry should offer neither discard nor split")
	}

	// Entirely outside, and entirely inside what is already recorded: neither
	// adds a question.
	for _, span := range [][2]time.Time{
		{at(f.now, 16, 0), at(f.now, 17, 0)},
		{at(f.now, 11, 0), at(f.now, 12, 0)},
	} {
		if recorded, err := f.svc.RecordIdle(f.ctx, entry.ID, span[0], span[1], domain.IdleAsleep); err != nil {
			t.Fatalf("record idle: %v", err)
		} else if recorded {
			t.Errorf("%s-%s should not have added an observation",
				span[0].Format("15:04"), span[1].Format("15:04"))
		}
	}
	if pending, _ := f.svc.PendingIdle(f.ctx); len(pending) != 1 {
		t.Errorf("%d observations after reporting the same absence again, want 1", len(pending))
	}

	// Under the threshold, which defaults to fifteen minutes.
	other, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: at(f.now, 15, 0),
		EndedAt: timePtr(at(f.now, 16, 0)), Billable: true,
	})
	if err != nil {
		t.Fatalf("record entry: %v", err)
	}
	if recorded, err := f.svc.RecordIdle(f.ctx, other.ID,
		at(f.now, 15, 10), at(f.now, 15, 15), domain.IdleAsleep); err != nil || recorded {
		t.Errorf("a five-minute stretch was recorded (%v, err %v); the threshold is fifteen",
			recorded, err)
	}
}

// TestIdleObservationIsNobodyElsesBusiness.
//
// An observation is evidence about one person's day that costs them hours if
// they accept it. Filing one against somebody else's entry, or answering one
// filed against theirs, is refused as a missing row rather than as a
// permission - an administrator has no more business here than anyone else.
func TestIdleObservationIsNobodyElsesBusiness(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	if _, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 12, 0), at(f.now, 13, 0), domain.IdleAsleep); err != nil {
		t.Fatalf("record idle: %v", err)
	}
	pending, _ := f.svc.PendingIdle(f.ctx)
	if len(pending) != 1 {
		t.Fatalf("%d pending, want 1", len(pending))
	}

	colleague, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Someone Else", Role: domain.RoleAdmin,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	theirs := auth.WithUser(f.ctx, colleague)

	if _, err := f.svc.RecordIdle(theirs, entry.ID,
		at(f.now, 10, 0), at(f.now, 11, 0), domain.IdleAsleep); !errors.Is(err, ErrNotFound) {
		t.Errorf("recording against another person's entry = %v, want not found", err)
	}
	if err := f.svc.ResolveIdle(theirs, pending[0].Observation.ID, domain.IdleDiscard); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolving another person's observation = %v, want not found", err)
	}
	if theirPending, err := f.svc.PendingIdle(theirs); err != nil || len(theirPending) != 0 {
		t.Errorf("a colleague sees %d of these observations (err %v), want 0", len(theirPending), err)
	}

	// And the hour is still on the original timesheet.
	day, _ := f.svc.Day(f.ctx, f.now)
	if day.Totals.SummedSeconds != 6*3600 {
		t.Errorf("the day totals %ds, want the full 6h", day.Totals.SummedSeconds)
	}
}

// TestIdleCannotReopenASubmittedWeek.
//
// Answering a prompt is an edit. Taking an hour out of a week somebody has
// approved by pressing "discard" would be the same change as taking it out by
// hand, and the week lock exists so that cannot happen quietly.
func TestIdleCannotReopenASubmittedWeek(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	if _, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 12, 0), at(f.now, 13, 0), domain.IdleAsleep); err != nil {
		t.Fatalf("record idle: %v", err)
	}
	pending, _ := f.svc.PendingIdle(f.ctx)
	if len(pending) != 1 {
		t.Fatalf("%d pending, want 1", len(pending))
	}

	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit the week: %v", err)
	}

	err := f.svc.ResolveIdle(f.ctx, pending[0].Observation.ID, domain.IdleDiscard)
	if err == nil {
		t.Fatal("discarding inside a submitted week should be refused")
	}
	day, _ := f.svc.Day(f.ctx, f.now)
	if day.Totals.SummedSeconds != 6*3600 {
		t.Errorf("the submitted week lost time to a refused resolution: %ds, want 6h",
			day.Totals.SummedSeconds)
	}
}

// TestIdleObservationOnARunningTimerIsANoticeNotAQuestion.
//
// A running timer's interval is still being measured, so there is nothing
// stable to rewrite. The observation is reported while it runs and becomes a
// question once it stops.
func TestIdleObservationOnARunningTimerIsANoticeNotAQuestion(t *testing.T) {
	f := newFixture(t)
	started, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "")
	if err != nil {
		t.Fatalf("start timer: %v", err)
	}
	f.advance(3 * time.Hour)

	if _, err := f.svc.RecordIdle(f.ctx, started.ID,
		f.now.Add(-2*time.Hour), f.now.Add(-time.Hour), domain.IdleAsleep); err != nil {
		t.Fatalf("record idle: %v", err)
	}

	running, err := f.svc.RunningIdle(f.ctx)
	if err != nil {
		t.Fatalf("running idle: %v", err)
	}
	if len(running) != 1 {
		t.Fatalf("%d notices on the running timer, want 1", len(running))
	}
	if pending, _ := f.svc.PendingIdle(f.ctx); len(pending) != 0 {
		t.Errorf("%d questions while the timer runs, want 0 until it stops", len(pending))
	}

	if _, err := f.svc.StopTimer(f.ctx, started.ID); err != nil {
		t.Fatalf("stop timer: %v", err)
	}
	if pending, _ := f.svc.PendingIdle(f.ctx); len(pending) != 1 {
		t.Errorf("%d questions after stopping, want 1", len(pending))
	}
	if running, _ := f.svc.RunningIdle(f.ctx); len(running) != 0 {
		t.Errorf("%d notices after stopping, want 0", len(running))
	}
}

// TestIdleObservationIsRefusedWhenTheFeatureIsOff.
//
// Zero turns it off, as zero turns off the long-timer flag. A page that has not
// noticed yet is told nothing came of its report rather than given an error.
func TestIdleObservationIsRefusedWhenTheFeatureIsOff(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 16, 0)
	entry := idleFixture(t, f)

	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", DefaultRounding: "none", DefaultRate: "0",
		WeekStart: 1, IdleDisabled: true,
	}); err != nil {
		t.Fatalf("switch idle detection off: %v", err)
	}

	recorded, err := f.svc.RecordIdle(f.ctx, entry.ID,
		at(f.now, 12, 0), at(f.now, 13, 0), domain.IdleAsleep)
	if err != nil {
		t.Fatalf("record idle with the feature off: %v", err)
	}
	if recorded {
		t.Error("an observation was stored with idle detection switched off")
	}
	if pending, _ := f.svc.PendingIdle(f.ctx); len(pending) != 0 {
		t.Errorf("%d pending with the feature off, want 0", len(pending))
	}
}
