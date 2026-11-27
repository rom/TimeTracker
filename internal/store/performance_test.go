package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The queries on the common page path must not walk the table.
//
// ASR-012's budgets are measured in internal/web/perf_test.go: a hundred
// thousand entries over three years, through the HTTP handler, with absolute
// budgets and the memory of an export asserted too. That suite is the proof.
// It is tagged `perf` and excluded from `make check` because it takes about a
// minute, which is the right trade for a suite that thorough - and it leaves a
// gap this one fills.
//
// The gap is that a regression lands in the ordinary suite unnoticed. A query
// that quietly stops using its index does not fail a test, does not change a
// screen, and gets slower in proportion to how long somebody has been using
// the application. It is found when a person says the day screen feels slow,
// which is how ADR-0032's three were found and how the 72 ms in
// ListRunningEntries was found.
//
// So this is the cheap, sensitive half, and it runs every time. Cheap: three
// seconds, twenty thousand entries. Sensitive: it times the queries on their
// own rather than through a render, because a page render is milliseconds of
// template work that drowns a query costing microseconds - removing the
// running-timer index makes that query sixty-four times slower and a whole
// render only about a third slower.
//
// It asserts a ratio rather than a stopwatch reading, which is what lets it
// run anywhere: "this query costs the same against twenty thousand entries as
// against a hundred" means the same thing on a laptop and on a loaded runner,
// and is the actual property. The absolute numbers belong to the tagged suite.
//
// Every query here runs on every page render. A scan in any of them is paid
// for by every screen, by everybody, all day - and the proxy inbox's was,
// until this test was written and found it.

const (
	perfProbeSmall = 100
	perfProbeBig   = 20000
)

// perfProbeTolerance is how much slower the query may be against two hundred
// times the data.
//
// An index seek does not move; a table scan is linear. Ten times is a wide
// margin for the first and nowhere near enough room for the second.
const perfProbeTolerance = 10.0

// perfProbeFloor absorbs the noise of a query measured in microseconds.
const perfProbeFloor = 150 * time.Microsecond

func TestThePageQueriesDoNotWalkTheTable(t *testing.T) {
	if testing.Short() {
		t.Skip("the performance probe seeds twenty thousand entries")
	}

	small, smallUser := seedEntries(t, perfProbeSmall)
	big, bigUser := seedEntries(t, perfProbeBig)
	ctx := context.Background()

	// The week the seeded data ends in, for the queries that take one.
	week := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")

	for _, probe := range []struct {
		name string
		run  func(db *DB, userID int64) error
	}{
		{
			// The one that was actually wrong, and the worst place for it:
			// the header runs this on every render of every screen.
			name: "the header's running timers",
			run: func(db *DB, userID int64) error {
				_, err := db.ListRunningEntries(ctx, userID)
				return err
			},
		},
		{
			// The week banner, which appears on every screen where time is
			// entered. This one was measured at 72 ms once already.
			name: "the week's total",
			run: func(db *DB, userID int64) error {
				_, err := db.WeekSeconds(ctx, userID, week)
				return err
			},
		},
		{
			name: "the proxy inbox count",
			run: func(db *DB, userID int64) error {
				_, err := db.ListEntriesEnteredBy(ctx, userID, UnrestrictedScope())
				return err
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			quick := fastestQuery(t, func() error { return probe.run(small, smallUser) })
			slow := fastestQuery(t, func() error { return probe.run(big, bigUser) })

			ratio := float64(slow) / float64(max(quick, time.Nanosecond))
			t.Logf("%s: %s at %d entries, %s at %d (%.1fx)",
				probe.name, quick, perfProbeSmall, slow, perfProbeBig, ratio)

			budget := time.Duration(float64(quick)*perfProbeTolerance) + perfProbeFloor
			if slow > budget {
				t.Errorf("%s took %s against %d entries and %s against %d - %.1fx.\n"+
					"That is the shape of a table scan. Every page render pays "+
					"for this query; check that the index it needs exists and "+
					"that the planner is using it.",
					probe.name, quick, perfProbeSmall, slow, perfProbeBig, ratio)
			}
		})
	}
}

// fastestQuery runs something repeatedly and returns the quickest run, which
// is the measurement with the least of the machine's noise in it.
func fastestQuery(t *testing.T, run func() error) time.Duration {
	t.Helper()

	if err := run(); err != nil {
		t.Fatalf("probe: %v", err)
	}
	best := time.Duration(1<<63 - 1)
	for range 20 {
		began := time.Now()
		err := run()
		took := time.Since(began)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if took < best {
			best = took
		}
	}
	return best
}

// seedEntries builds a database holding a given number of finished entries.
//
// Finished matters: an entry with no end time is an open timer, and a database
// full of them would make the running-timer probe above return twenty thousand
// rows rather than none - measuring the wrong thing, and passing for it.
func seedEntries(t *testing.T, entries int) (*DB, int64) {
	t.Helper()

	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	now := time.Now().UTC().Truncate(time.Hour)
	for i := range entries {
		started := now.Add(-time.Duration(i) * 40 * time.Minute)
		ended := started.Add(30 * time.Minute)
		if _, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt: started, EndedAt: &ended, DurationSeconds: 1800,
			Note: fmt.Sprintf("Work item %d", i), Billable: true,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
		}); err != nil {
			t.Fatalf("seed entry %d: %v", i, err)
		}
	}
	return db, user.ID
}
