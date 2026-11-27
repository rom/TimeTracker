package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Summing a range, and correcting an absence.
//
// Both are small queries, and both are the kind that is wrong at the edges or
// not at all: an off-by-one day at either end of a range is invisible in a
// month and wrong in a balance.

// daysOfWork records an hour on each of the given days.
func daysOfWork(t *testing.T, db *DB, user domain.User, assignment domain.Assignment, days ...string) {
	t.Helper()

	ctx := context.Background()
	for _, day := range days {
		start, err := time.Parse(time.RFC3339, day+"T09:00:00Z")
		if err != nil {
			t.Fatalf("parse %s: %v", day, err)
		}
		if _, err := db.CreateEntry(ctx, domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt: start, DurationSeconds: 3600, Note: day,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
		}); err != nil {
			t.Fatalf("create entry on %s: %v", day, err)
		}
	}
}

func TestARangeIncludesItsFirstDayAndExcludesItsLast(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	daysOfWork(t, db, user, assignment,
		"2026-03-08", "2026-03-09", "2026-03-14", "2026-03-15", "2026-03-16")

	// A week, Monday the 9th to Sunday the 15th. The 8th is the day before it
	// and the 16th the day after; both have an hour on them, and neither may
	// be counted - the end is exclusive, as it is in every range in this
	// application.
	total, err := db.RangeSeconds(ctx, user.ID, "2026-03-09", "2026-03-16")
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if total != 3*3600 {
		t.Errorf("the week totals %d seconds, want %d: the range must include "+
			"the 9th and exclude the 16th", total, 3*3600)
	}

	// An empty range is nothing rather than an error, which is what a balance
	// asks for on its first week.
	if total, err = db.RangeSeconds(ctx, user.ID, "2026-04-01", "2026-04-08"); err != nil {
		t.Fatalf("empty range: %v", err)
	}
	if total != 0 {
		t.Errorf("an empty range totals %d, want 0", total)
	}
}

// Only time that counts: a proposal nobody has accepted, and a flagged entry,
// are both excluded from a week total and must be excluded from a range for
// the same reason - a balance built on a different rule than the week banner
// beside it would disagree with it.
func TestARangeCountsWhatAWeekCounts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	base := time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC)
	for _, entry := range []domain.TimeEntry{
		{StartedAt: base, Status: domain.StatusConfirmed},
		{StartedAt: base.AddDate(0, 0, 1), Status: domain.StatusPending},
		{StartedAt: base.AddDate(0, 0, 2), Status: domain.StatusConfirmed, Flagged: true},
	} {
		entry.UserID = user.ID
		entry.EnteredBy = user.ID
		entry.AssignmentID = assignment.ID
		entry.DurationSeconds = 3600
		entry.TimeZone = "UTC"
		if _, err := db.CreateEntry(ctx, entry); err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}

	total, err := db.RangeSeconds(ctx, user.ID, "2026-03-09", "2026-03-16")
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	week, err := db.WeekSeconds(ctx, user.ID, "2026-03-09")
	if err != nil {
		t.Fatalf("week: %v", err)
	}
	if total != week {
		t.Errorf("the range totals %d and the week %d; a balance that counted "+
			"differently from the banner beside it would be argued with",
			total, week)
	}
	if total != 3600 {
		t.Errorf("totalled %d, want one confirmed unflagged hour", total)
	}
}

func TestCorrectingAnAbsenceChangesItsSpanAndNothingElse(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	created, err := CreateAbsenceTx(ctx, db.write, domain.Absence{
		UserID: user.ID, Kind: domain.AbsenceSick,
		StartsOn: "2026-03-16", EndsOn: "2026-03-16", Note: "Flu",
		CreatedBy: user.ID, CreatedAt: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return UpdateAbsenceTx(ctx, tx, domain.Absence{
			ID: created.ID, Kind: domain.AbsenceSick,
			StartsOn: "2026-03-16", EndsOn: "2026-03-18",
			Note: "Flu, and it kept going",
			// Deliberately wrong, to prove they are not written: an edit
			// corrects what was recorded, not whose record it is.
			UserID: 999, CreatedBy: 999,
		})
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	after, err := db.GetAbsence(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.EndsOn != "2026-03-18" || after.Note != "Flu, and it kept going" {
		t.Errorf("the correction did not land: %s–%s %q",
			after.StartsOn, after.EndsOn, after.Note)
	}
	if after.UserID != user.ID || after.CreatedBy != user.ID {
		t.Errorf("the correction moved the record to user %d, recorded by %d: "+
			"moving an absence to another person is a deletion and a new "+
			"record, not an edit", after.UserID, after.CreatedBy)
	}
}
