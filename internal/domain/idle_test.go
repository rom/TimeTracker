package domain

import (
	"testing"
	"time"
)

// Idle resolution decides how much of a day survives, so every case is pinned
// as an interval rather than only as a total: a duration that is right while the
// interval is wrong shows up on the timeline, not in the numbers.

func at(hour, minute int) time.Time {
	return time.Date(2026, 5, 12, hour, minute, 0, 0, time.UTC)
}

// stopped builds an entry with a known interval.
func stopped(from, to time.Time) TimeEntry {
	end := to
	return TimeEntry{ID: 7, UserID: 1, StartedAt: from, EndedAt: &end,
		DurationSeconds: SecondsBetween(from, to)}
}

func observed(from, to time.Time) IdleObservation {
	return IdleObservation{ID: 3, EntryID: 7, UserID: 1,
		StartedAt: from, EndedAt: to, Source: IdleAsleep}
}

func TestResolveIdle(t *testing.T) {
	// 09:00-15:00 with the application seeing nothing over lunch, 12:00-13:00.
	entry := stopped(at(9, 0), at(15, 0))
	lunch := observed(at(12, 0), at(13, 0))

	cases := []struct {
		name       string
		entry      TimeEntry
		observed   IdleObservation
		resolution IdleResolution
		want       IdleOutcome
	}{
		{
			name: "keep changes nothing", entry: entry, observed: lunch, resolution: IdleKeep,
			want: IdleOutcome{KeepFrom: at(9, 0), KeepTo: at(15, 0), KeptSeconds: 6 * 3600},
		},
		{
			// Discard means "and everything after it", which for an interior
			// stretch is the afternoon as well as the lunch hour. That is the
			// dangerous one, and the reason the interface has to say so.
			name:  "discard ends the entry where the stretch began",
			entry: entry, observed: lunch, resolution: IdleDiscard,
			want: IdleOutcome{KeepFrom: at(9, 0), KeepTo: at(12, 0),
				KeptSeconds: 3 * 3600, RemovedSeconds: 3 * 3600},
		},
		{
			name:  "split keeps the work on both sides",
			entry: entry, observed: lunch, resolution: IdleSplit,
			want: IdleOutcome{KeepFrom: at(9, 0), KeepTo: at(12, 0),
				SplitFrom: at(13, 0), SplitTo: at(15, 0), Splits: true,
				KeptSeconds: 5 * 3600, RemovedSeconds: 3600},
		},
		{
			// Nothing followed the stretch, so a split has no second entry to
			// make. It is the same request as a discard and is answered as one.
			name:  "a trailing stretch splits into a trim",
			entry: stopped(at(9, 0), at(15, 0)), observed: observed(at(13, 0), at(15, 0)),
			resolution: IdleSplit,
			want: IdleOutcome{KeepFrom: at(9, 0), KeepTo: at(13, 0),
				KeptSeconds: 4 * 3600, RemovedSeconds: 2 * 3600},
		},
		{
			// A stretch at the very start shortens the entry from the front,
			// which is the one case where the entry's start moves.
			name:  "a leading stretch moves the start",
			entry: stopped(at(9, 0), at(15, 0)), observed: observed(at(9, 0), at(10, 30)),
			resolution: IdleSplit,
			want: IdleOutcome{KeepFrom: at(10, 30), KeepTo: at(15, 0),
				KeptSeconds: 4*3600 + 1800, RemovedSeconds: 5400},
		},
		{
			// Discarding a leading stretch would mean discarding the entry, so
			// it is treated as the split it can only be. Losing the whole day to
			// a mistaken button is a worse failure than ignoring the "and
			// everything after" half of a discard.
			name:  "discarding a leading stretch does not empty the entry",
			entry: stopped(at(9, 0), at(15, 0)), observed: observed(at(9, 0), at(10, 30)),
			resolution: IdleDiscard,
			want: IdleOutcome{KeepFrom: at(10, 30), KeepTo: at(15, 0),
				KeptSeconds: 4*3600 + 1800, RemovedSeconds: 5400},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveIdle(c.entry, c.observed, c.resolution)
			if err != nil {
				t.Fatalf("ResolveIdle: %v", err)
			}
			if !got.KeepFrom.Equal(c.want.KeepFrom) || !got.KeepTo.Equal(c.want.KeepTo) {
				t.Errorf("kept interval = %s-%s, want %s-%s",
					got.KeepFrom.Format("15:04"), got.KeepTo.Format("15:04"),
					c.want.KeepFrom.Format("15:04"), c.want.KeepTo.Format("15:04"))
			}
			if got.Splits != c.want.Splits {
				t.Errorf("Splits = %v, want %v", got.Splits, c.want.Splits)
			}
			if got.Splits && (!got.SplitFrom.Equal(c.want.SplitFrom) || !got.SplitTo.Equal(c.want.SplitTo)) {
				t.Errorf("split interval = %s-%s, want %s-%s",
					got.SplitFrom.Format("15:04"), got.SplitTo.Format("15:04"),
					c.want.SplitFrom.Format("15:04"), c.want.SplitTo.Format("15:04"))
			}
			if got.KeptSeconds != c.want.KeptSeconds {
				t.Errorf("KeptSeconds = %d, want %d", got.KeptSeconds, c.want.KeptSeconds)
			}
			if got.RemovedSeconds != c.want.RemovedSeconds {
				t.Errorf("RemovedSeconds = %d, want %d", got.RemovedSeconds, c.want.RemovedSeconds)
			}
		})
	}
}

// TestResolveIdleConservesTime.
//
// Whatever the resolution, what is kept plus what is removed is the entry. A
// resolution that loses a minute to arithmetic would show up as a timesheet
// that no longer adds up to the day, which is the one thing a tracker may not
// do - and is exactly the sort of error a table of expected values can agree
// with, because both were written by the same person on the same afternoon.
func TestResolveIdleConservesTime(t *testing.T) {
	entry := stopped(at(8, 0), at(17, 0))
	total := int64(9 * 3600)

	for _, stretch := range []IdleObservation{
		observed(at(8, 0), at(9, 0)),   // leading
		observed(at(12, 0), at(13, 0)), // interior
		observed(at(16, 0), at(17, 0)), // trailing
		observed(at(8, 30), at(16, 30)),
	} {
		for _, resolution := range []IdleResolution{IdleKeep, IdleDiscard, IdleSplit} {
			outcome, err := ResolveIdle(entry, stretch, resolution)
			if err != nil {
				t.Fatalf("%s of %s: %v", resolution,
					stretch.StartedAt.Format("15:04"), err)
			}
			if outcome.KeptSeconds+outcome.RemovedSeconds != total {
				t.Errorf("%s of %s-%s: kept %d + removed %d != %d",
					resolution, stretch.StartedAt.Format("15:04"),
					stretch.EndedAt.Format("15:04"),
					outcome.KeptSeconds, outcome.RemovedSeconds, total)
			}
			// The intervals have to agree with the totals, not merely exist.
			counted := SecondsBetween(outcome.KeepFrom, outcome.KeepTo)
			if outcome.Splits {
				counted += SecondsBetween(outcome.SplitFrom, outcome.SplitTo)
			}
			if counted != outcome.KeptSeconds {
				t.Errorf("%s of %s: intervals cover %ds but KeptSeconds is %d",
					resolution, stretch.StartedAt.Format("15:04"), counted, outcome.KeptSeconds)
			}
		}
	}
}

// TestResolveIdleRefusesWhatItCannotDo.
//
// Two refusals matter. A running timer is still being measured, so rewriting
// its interval would race the clock. And a stretch covering the whole entry
// leaves nothing to keep: emptying the row silently would be a deletion nobody
// asked for.
func TestResolveIdleRefusesWhatItCannotDo(t *testing.T) {
	running := TimeEntry{ID: 7, UserID: 1, StartedAt: at(9, 0)}
	if _, err := ResolveIdle(running, observed(at(9, 30), at(10, 0)), IdleSplit); err == nil {
		t.Error("resolving against a running timer should be refused")
	}

	whole := stopped(at(9, 0), at(10, 0))
	if _, err := ResolveIdle(whole, observed(at(9, 0), at(10, 0)), IdleDiscard); err == nil {
		t.Error("a stretch covering the entry should be refused, not silently emptied")
	}

	if _, err := ResolveIdle(whole, observed(at(9, 10), at(9, 20)), "erase"); err == nil {
		t.Error("an unknown resolution should be refused")
	}
}

// TestClampIdle.
//
// The times arrive from a browser. A clock a few minutes out is ordinary, so
// the stretch is fitted to the entry rather than rejected - but it must never
// end up covering time the entry does not, or claiming time that has not
// happened yet.
func TestClampIdle(t *testing.T) {
	now := at(15, 0)
	entry := stopped(at(9, 0), at(14, 0))

	from, to, ok := ClampIdle(entry, at(8, 0), at(20, 0), now, 60)
	if !ok {
		t.Fatal("a stretch overlapping the entry should be clamped, not dropped")
	}
	if !from.Equal(at(9, 0)) || !to.Equal(at(14, 0)) {
		t.Errorf("clamped to %s-%s, want 09:00-14:00",
			from.Format("15:04"), to.Format("15:04"))
	}

	if _, _, ok := ClampIdle(entry, at(16, 0), at(17, 0), now, 60); ok {
		t.Error("a stretch entirely outside the entry should be dropped")
	}
	if _, _, ok := ClampIdle(entry, at(10, 0), at(10, 0), now, 60); ok {
		t.Error("an empty stretch should be dropped")
	}
	if _, _, ok := ClampIdle(entry, at(10, 0), at(10, 0).Add(30*time.Second), now, 60); ok {
		t.Error("a stretch under the threshold should be dropped")
	}

	// A running timer is bounded by the present, so a page reporting a stretch
	// that runs into the future cannot record time nobody has spent.
	running := TimeEntry{ID: 7, UserID: 1, StartedAt: at(9, 0)}
	_, to, ok = ClampIdle(running, at(13, 0), at(23, 0), now, 60)
	if !ok || !to.Equal(now) {
		t.Errorf("a running timer's stretch ends at now; got %s (ok=%v)",
			to.Format("15:04"), ok)
	}
}
