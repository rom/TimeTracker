package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The plumbing the rest of the store is built on.
//
// Timestamp parsing, the transactional reads a read-modify-write sequence needs,
// the search index rebuild, and the handles the rest of the application is given.
// None of it is interesting on its own, and all of it is load-bearing.

// TestStoredTimestampsSurviveBothSpellings.
//
// Every stored instant goes through this. We write RFC 3339 to the second, but a
// database restored from a backup or touched by an external sqlite3 session can
// carry nanoseconds, and a row that fails to parse is a row the application
// cannot show at all.
func TestStoredTimestampsSurviveBothSpellings(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"what we write", "2026-03-16T09:30:00Z",
			time.Date(2026, 3, 16, 9, 30, 0, 0, time.UTC)},
		{"nanoseconds from somewhere else", "2026-03-16T09:30:00.123456789Z",
			time.Date(2026, 3, 16, 9, 30, 0, 123456789, time.UTC)},
		{"an offset, normalised to UTC", "2026-03-16T10:30:00+01:00",
			time.Date(2026, 3, 16, 9, 30, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTime(tc.input)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.input, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parsed %q as %s, want %s", tc.input, got, tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("parsed %q into %s, want UTC - a stored instant has no zone",
					tc.input, got.Location())
			}
		})
	}

	// The empty string is "not set" rather than a parse failure: it is what an
	// optional column holds.
	if got, err := parseTime(""); err != nil || !got.IsZero() {
		t.Errorf("parseTime(\"\") = %s, %v; want the zero time and no error", got, err)
	}

	// Anything else is reported, with the value in the message, because a
	// corrupt timestamp is only findable if the error says which one it was.
	_, err := parseTime("16 March 2026")
	if err == nil {
		t.Fatal("an unparseable timestamp was accepted")
	}
	if !strings.Contains(err.Error(), "16 March 2026") {
		t.Errorf("error %q does not name the value that failed", err)
	}

	// And a round trip through what we write loses nothing above the second.
	original := time.Date(2026, 3, 16, 9, 30, 0, 0, time.FixedZone("CET", 3600))
	back, err := parseTime(formatTime(original))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if !back.Equal(original) {
		t.Errorf("round trip gave %s, want %s", back, original)
	}
}

// TestAnOptionalTimestampIsAbsentOrAnInstant.
//
// The nullable columns - archived_at, ended_at, resolved_at - all read through
// this. A running timer whose ended_at came back as the zero instant rather
// than as absent would look like a timer that stopped in year one.
func TestAnOptionalTimestampIsAbsentOrAnInstant(t *testing.T) {
	absent, err := nullableTime(sql.NullString{})
	if err != nil {
		t.Fatalf("null: %v", err)
	}
	if absent != nil {
		t.Errorf("a NULL column read as %s, want absent", absent)
	}

	// An empty string is the same thing: SQLite is content to hold one, and a
	// column written before it was nullable does.
	if empty, err := nullableTime(sql.NullString{String: "", Valid: true}); err != nil {
		t.Fatalf("empty: %v", err)
	} else if empty != nil {
		t.Errorf("an empty column read as %s, want absent", empty)
	}

	present, err := nullableTime(sql.NullString{String: "2026-03-16T09:00:00Z", Valid: true})
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if present == nil || !present.Equal(time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("a set column read as %v", present)
	}

	if _, err := nullableTime(sql.NullString{String: "whenever", Valid: true}); err == nil {
		t.Error("an unparseable optional timestamp was accepted")
	}
}

// TestTheStoreSaysWhereItIsAndLendsItsReader.
//
// Diagnostics name the file, and the health check and the backup read through
// the pooled reader rather than opening their own connection. Both are one-line
// accessors, and both are what a support conversation starts from.
func TestTheStoreSaysWhereItIsAndLendsItsReader(t *testing.T) {
	db := newTestDB(t)

	if !strings.HasSuffix(db.Path(), "test.db") {
		t.Errorf("Path() = %q, want the file the store was opened on", db.Path())
	}

	reader := db.Reader()
	if reader == nil {
		t.Fatal("Reader() handed out nothing")
	}
	var one int
	if err := reader.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the lent reader cannot read: %v", err)
	}
	if one != 1 {
		t.Errorf("the lent reader answered %d", one)
	}
}

// TestATransactionSeesItsOwnUncommittedWrite.
//
// This is why GetEntryTx exists. The read pool is a separate connection and
// cannot see an open transaction's rows, so a read-modify-write that went
// through the usual reader would modify a stale value - and the whole point of
// doing both in one transaction would be lost.
func TestATransactionSeesItsOwnUncommittedWrite(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	entry := entryAt(t, db, user, assignment,
		time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), 3600, domain.StatusConfirmed)

	err := db.InTx(ctx, func(tx *sql.Tx) error {
		inside, err := GetEntryTx(ctx, tx, entry.ID)
		if err != nil {
			return err
		}
		if inside.ID != entry.ID {
			t.Errorf("read entry %d inside the transaction, want %d", inside.ID, entry.ID)
		}
		if inside.AssignmentName == "" {
			t.Error("the transactional read lost the joined names the listing has")
		}

		inside.Note = "Rewritten in the transaction"
		if err := UpdateEntryTx(ctx, tx, inside); err != nil {
			return err
		}

		// The same read now sees the new value, which is the sequence this
		// function exists for.
		again, err := GetEntryTx(ctx, tx, entry.ID)
		if err != nil {
			return err
		}
		if again.Note != "Rewritten in the transaction" {
			t.Errorf("the transaction cannot see its own write: note = %q", again.Note)
		}

		// And the read pool cannot, until the commit.
		outside, err := db.GetEntry(ctx, entry.ID)
		if err != nil {
			return err
		}
		if outside.Note != "" {
			t.Errorf("an uncommitted note reached the read pool: %q", outside.Note)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}

	committed, err := db.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("read after commit: %v", err)
	}
	if committed.Note != "Rewritten in the transaction" {
		t.Errorf("after the commit the note is %q", committed.Note)
	}

	// A missing entry is not found rather than a scan error, inside a
	// transaction as much as outside one.
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		_, err := GetEntryTx(ctx, tx, 404)
		return err
	})
	if err == nil {
		t.Error("reading a nonexistent entry in a transaction succeeded")
	}
}

// TestRewritingAnEntryClosesTheQuestionsAboutIt.
//
// An idle observation is a question about a stretch of an entry. When the entry
// is deleted or its interval is rewritten by hand, the question is about an
// interval that no longer exists - and answering it later would apply the answer
// to the wrong times.
func TestRewritingAnEntryClosesTheQuestionsAboutIt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entry := entryAt(t, db, user, assignment, start, 4*3600, domain.StatusConfirmed)
	other := entryAt(t, db, user, assignment, start.AddDate(0, 0, 1), 4*3600, domain.StatusConfirmed)

	ask := func(e domain.TimeEntry, from, to time.Time) int64 {
		t.Helper()
		id, err := db.CreateIdleObservation(ctx, domain.IdleObservation{
			EntryID: e.ID, UserID: user.ID, StartedAt: from, EndedAt: to,
			Source: domain.IdleUntouched,
		})
		if err != nil {
			t.Fatalf("record observation: %v", err)
		}
		return id
	}

	first := ask(entry, start.Add(time.Hour), start.Add(2*time.Hour))
	second := ask(entry, start.Add(3*time.Hour), start.Add(4*time.Hour))
	elsewhere := ask(other, start.AddDate(0, 0, 1).Add(time.Hour),
		start.AddDate(0, 0, 1).Add(2*time.Hour))

	open, err := db.UnresolvedIdleObservations(ctx, user.ID, 20)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(open) != 3 {
		t.Fatalf("%d open questions, want 3", len(open))
	}

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return ResolveIdleObservationsForEntryTx(ctx, tx, entry.ID, domain.IdleKeep, time.Now())
	})
	if err != nil {
		t.Fatalf("close questions: %v", err)
	}

	open, err = db.UnresolvedIdleObservations(ctx, user.ID, 20)
	if err != nil {
		t.Fatalf("re-list open: %v", err)
	}
	if len(open) != 1 || open[0].ID != elsewhere {
		t.Fatalf("after closing one entry's questions %d remain open (%+v), "+
			"want only the other entry's", len(open), open)
	}

	for _, id := range []int64{first, second} {
		closed, err := db.GetIdleObservation(ctx, id)
		if err != nil {
			t.Fatalf("read observation %d: %v", id, err)
		}
		if closed.Resolution != domain.IdleKeep {
			t.Errorf("observation %d resolution = %q, want keep", id, closed.Resolution)
		}
		if closed.ResolvedAt.IsZero() {
			t.Errorf("observation %d was resolved without a time", id)
		}
	}

	// Closing them again is not an error, because an entry can be edited twice
	// and the second edit has nothing left to close.
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return ResolveIdleObservationsForEntryTx(ctx, tx, entry.ID, domain.IdleDiscard, time.Now())
	})
	if err != nil {
		t.Fatalf("closing twice: %v", err)
	}
	// And it must not overwrite the answer already given.
	unchanged, err := db.GetIdleObservation(ctx, first)
	if err != nil {
		t.Fatalf("re-read observation: %v", err)
	}
	if unchanged.Resolution != domain.IdleKeep {
		t.Errorf("a second close rewrote an answered question to %q", unchanged.Resolution)
	}
}

// TestRebuildingTheIndexInsideATransactionSeesTheChange.
//
// A tag rename and the index rebuild that follows it have to be one
// transaction. The rename is invisible to another connection until the commit,
// so a rebuild running beside it would index the old name and quietly undo the
// rename as far as searching is concerned.
func TestRebuildingTheIndexInsideATransactionSeesTheChange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	entry := entryAt(t, db, user, assignment,
		time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), 3600, domain.StatusConfirmed)
	retag(t, db, entry, "incident")

	finds := func(t *testing.T, query string) int {
		t.Helper()
		entries, err := db.ListEntries(ctx, EntryFilter{Scope: UnrestrictedScope(), Query: query})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		return len(entries)
	}
	if finds(t, "incident") != 1 {
		t.Fatal("the tag is not searchable before the rename, so this proves nothing")
	}

	// Rename and rebuild in one transaction, the way the tag editor does.
	tags, err := db.ListTags(ctx, UnrestrictedScope())
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	tag := tags[0]
	tag.Name = "outage"

	var indexed int
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		if err := UpdateTagTx(ctx, tx, tag); err != nil {
			return err
		}
		n, err := ReindexSearchTx(ctx, tx)
		indexed = n
		return err
	})
	if err != nil {
		t.Fatalf("rename and rebuild: %v", err)
	}
	if indexed != 1 {
		t.Errorf("the rebuild indexed %d entries, want 1", indexed)
	}

	if finds(t, "outage") != 1 {
		t.Error("the new tag name is not searchable, so the rebuild ran before the rename was visible")
	}
	if finds(t, "incident") != 0 {
		t.Error("the old tag name is still searchable after the rename")
	}

	// The standalone rebuild is the same work on its own connection, and must
	// leave the index in the same state rather than emptying it.
	count, err := db.ReindexSearch(ctx)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if count != 1 {
		t.Errorf("a standalone rebuild indexed %d entries, want 1", count)
	}
	if finds(t, "outage") != 1 {
		t.Error("a standalone rebuild lost the entry")
	}
}

// TestArchivedCatalogueEntriesAreHiddenButReportable.
//
// Archiving takes something off the forms without deleting its history. A
// report over last year still has to be able to name the customer, so the
// listing has to be able to include archived rows on request - and must not by
// default, or archiving would do nothing visible.
func TestArchivedCatalogueEntriesAreHiddenButReportable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_, assignment := seed(t, db)

	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	count := func(t *testing.T, includeArchived bool) int {
		t.Helper()
		customers, err := db.ListCustomers(ctx, UnrestrictedScope(), includeArchived)
		if err != nil {
			t.Fatalf("list customers: %v", err)
		}
		return len(customers)
	}

	if count(t, false) != 1 {
		t.Fatalf("%d customers before archiving, want 1", count(t, false))
	}

	if err := db.SetCustomerArchived(ctx, project.CustomerID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if n := count(t, false); n != 0 {
		t.Errorf("%d customers on the form after archiving the only one, want 0", n)
	}
	if n := count(t, true); n != 1 {
		t.Errorf("%d customers in a historical report, want the archived 1", n)
	}

	// The archived row says when, so a screen can show it.
	archived, err := db.GetCustomer(ctx, project.CustomerID)
	if err != nil {
		t.Fatalf("read customer: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Error("an archived customer carries no archival date")
	}

	// Restoring puts it back on the form.
	if err := db.SetCustomerArchived(ctx, project.CustomerID, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n := count(t, false); n != 1 {
		t.Errorf("%d customers after restoring, want 1", n)
	}

	// A client's scope still narrows to their own customer, archived or not.
	scoped, err := db.ListCustomers(ctx, Scope{CustomerID: project.CustomerID}, false)
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	if len(scoped) != 1 {
		t.Errorf("a client saw %d customers, want their own 1", len(scoped))
	}
	nobody, err := db.ListCustomers(ctx, Scope{}, false)
	if err != nil {
		t.Fatalf("empty scoped list: %v", err)
	}
	if len(nobody) != 0 {
		t.Errorf("an empty scope saw %d customers", len(nobody))
	}
}

// TestAPersonsOwnWeeksAreNewestFirstAndCapped.
//
// The timesheet history screen. Newest first because the week somebody wants is
// almost always the last one, and capped because this list grows for ever.
func TestAPersonsOwnWeeksAreNewestFirstAndCapped(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	weeks := []string{"2026-01-05", "2026-01-12", "2026-01-19", "2026-01-26"}
	for _, week := range weeks {
		if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
			UserID: user.ID, WeekStart: week, Status: domain.PeriodApproved,
		}); err != nil {
			t.Fatalf("store %s: %v", week, err)
		}
	}

	listed, err := db.ListPeriodsForUser(ctx, user.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != len(weeks) {
		t.Fatalf("%d weeks, want %d", len(listed), len(weeks))
	}
	if listed[0].WeekStart != "2026-01-26" {
		t.Errorf("the first week shown is %s, want the most recent 2026-01-26",
			listed[0].WeekStart)
	}

	capped, err := db.ListPeriodsForUser(ctx, user.ID, 2)
	if err != nil {
		t.Fatalf("capped list: %v", err)
	}
	if len(capped) != 2 || capped[0].WeekStart != "2026-01-26" {
		t.Errorf("a limit of 2 gave %d weeks starting %s, want the 2 most recent",
			len(capped), capped[0].WeekStart)
	}

	// Nobody else's weeks are in it.
	other, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Someone Else", Role: domain.RoleMember, TimeZone: "UTC",
		Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	theirs, err := db.ListPeriodsForUser(ctx, other.ID, 0)
	if err != nil {
		t.Fatalf("their list: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("somebody with no weeks has %d", len(theirs))
	}

	// Upserting the same week twice is one row, so a resubmission is an edit
	// rather than a duplicate.
	if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
		UserID: user.ID, WeekStart: "2026-01-26", Status: domain.PeriodSubmitted,
		SubmittedSeconds: 3600,
	}); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	again, err := db.ListPeriodsForUser(ctx, user.ID, 0)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if len(again) != len(weeks) {
		t.Errorf("%d weeks after resubmitting one, want %d", len(again), len(weeks))
	}
	if again[0].Status != domain.PeriodSubmitted || again[0].SubmittedSeconds != 3600 {
		t.Errorf("the resubmission did not take: %+v", again[0])
	}
}

// TestADeadDatabaseIsAnErrorAndNeverAnEmptyAnswer.
//
// Every read in this package wraps its failure and returns it. That matters
// more than it looks: a listing that swallowed the error and returned no rows
// would put "no entries this week" in front of somebody whose database is
// unreachable, and they would believe it. An hour of work looks exactly like a
// broken disk if the difference is discarded here.
//
// Closing the handles is the cheapest way to make every query fail at once. The
// assertion is uniform - an error, and no rows - so a reader added later that
// forgets to check err is caught by being added to this list.
func TestADeadDatabaseIsAnErrorAndNeverAnEmptyAnswer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	// Everything below runs against closed handles.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// reads is every listing and lookup a screen renders, each returning the
	// number of rows it produced so "no error" and "no rows" can be told apart.
	reads := map[string]func() (int, error){
		"list entries": func() (int, error) {
			out, err := db.ListEntries(ctx, EntryFilter{Scope: UnrestrictedScope()})
			return len(out), err
		},
		"count entries": func() (int, error) {
			return db.CountEntries(ctx, EntryFilter{Scope: UnrestrictedScope()})
		},
		"get entry": func() (int, error) {
			_, err := db.GetEntry(ctx, 1)
			return 0, err
		},
		"list running entries": func() (int, error) {
			out, err := db.ListRunningEntries(ctx, user.ID)
			return len(out), err
		},
		"list customers": func() (int, error) {
			out, err := db.ListCustomers(ctx, UnrestrictedScope(), false)
			return len(out), err
		},
		"list projects": func() (int, error) {
			out, err := db.ListProjects(ctx, UnrestrictedScope(), 0, false)
			return len(out), err
		},
		"list assignments": func() (int, error) {
			out, err := db.ListAssignments(ctx, UnrestrictedScope(), 0, false)
			return len(out), err
		},
		"get customer": func() (int, error) {
			_, err := db.GetCustomer(ctx, project.CustomerID)
			return 0, err
		},
		"get project": func() (int, error) {
			_, err := db.GetProject(ctx, project.ID)
			return 0, err
		},
		"get assignment": func() (int, error) {
			_, err := db.GetAssignment(ctx, assignment.ID)
			return 0, err
		},
		"list tags": func() (int, error) {
			out, err := db.ListTags(ctx, UnrestrictedScope())
			return len(out), err
		},
		"tags for entries": func() (int, error) {
			out, err := db.TagsForEntries(ctx, []int64{1})
			return len(out), err
		},
		"list users": func() (int, error) {
			out, err := db.ListUsers(ctx)
			return len(out), err
		},
		"get user": func() (int, error) {
			_, err := db.GetUser(ctx, user.ID)
			return 0, err
		},
		"project ids for user": func() (int, error) {
			out, err := db.ProjectIDsForUser(ctx, user.ID)
			return len(out), err
		},
		"is project member": func() (int, error) {
			_, err := db.IsProjectMember(ctx, user.ID, project.ID)
			return 0, err
		},
		"list project members": func() (int, error) {
			out, err := db.ListProjectMembers(ctx, project.ID)
			return len(out), err
		},
		"member rate": func() (int, error) {
			_, err := db.MemberRateMinor(ctx, user.ID, project.ID)
			return 0, err
		},
		"consumption by project": func() (int, error) {
			out, err := db.ConsumptionByProject(ctx, UnrestrictedScope(), time.Time{}, time.Time{})
			return len(out), err
		},
		"active weeks by project": func() (int, error) {
			out, err := db.ActiveWeeksByProject(ctx, UnrestrictedScope(), time.Time{}, time.Time{})
			return len(out), err
		},
		"list periods for user": func() (int, error) {
			out, err := db.ListPeriodsForUser(ctx, user.ID, 0)
			return len(out), err
		},
		"list periods by status": func() (int, error) {
			out, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted, UnrestrictedScope())
			return len(out), err
		},
		"list periods in range": func() (int, error) {
			out, err := db.ListPeriodsInRange(ctx, "2026-01-01", "2027-01-01", UnrestrictedScope())
			return len(out), err
		},
		"get period": func() (int, error) {
			_, err := db.GetPeriod(ctx, user.ID, "2026-03-16")
			return 0, err
		},
		"week seconds": func() (int, error) {
			n, err := db.WeekSeconds(ctx, user.ID, "2026-03-16")
			return int(n), err
		},
		"list routines": func() (int, error) {
			out, err := db.ListRoutines(ctx, user.ID, false)
			return len(out), err
		},
		"quick-start assignments": func() (int, error) {
			out, err := db.QuickStartAssignments(ctx, user.ID, time.Time{}, 0)
			return len(out), err
		},
		"list contract terms": func() (int, error) {
			out, err := db.ListContractTerms(ctx, domain.TermsForCustomer, project.CustomerID)
			return len(out), err
		},
		"resolve terms": func() (int, error) {
			_, err := db.ResolveTerms(ctx, project.CustomerID, project.ID, "2026-03-16")
			return 0, err
		},
		"unresolved idle observations": func() (int, error) {
			out, err := db.UnresolvedIdleObservations(ctx, user.ID, 20)
			return len(out), err
		},
		"running idle observations": func() (int, error) {
			out, err := db.RunningIdleObservations(ctx, user.ID)
			return len(out), err
		},
		"dismissed reminders": func() (int, error) {
			out, err := db.DismissedReminders(ctx, user.ID, []string{"2026-03-16"})
			return len(out), err
		},
		"settings": func() (int, error) {
			_, err := db.GetSettings(ctx)
			return 0, err
		},
		"entries entered by": func() (int, error) {
			out, err := db.ListEntriesEnteredBy(ctx, user.ID, UnrestrictedScope())
			return len(out), err
		},
		"schema version": func() (int, error) {
			n, err := db.SchemaVersion(ctx)
			return n, err
		},
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			rows, err := read()
			if err == nil {
				t.Fatalf("%s succeeded against a closed database, returning %d rows - "+
					"a screen would show that as the truth", name, rows)
			}
			if rows != 0 {
				t.Errorf("%s returned %d rows alongside its error", name, rows)
			}
		})
	}

	// Writes are the same, and a transaction cannot even begin.
	if err := db.InTx(ctx, func(*sql.Tx) error {
		t.Error("the transaction body ran against a closed database")
		return nil
	}); err == nil {
		t.Error("a transaction against a closed database committed")
	}

	if _, err := db.CreateCustomer(ctx, domain.Customer{Name: "Nope", Currency: "EUR"}); err == nil {
		t.Error("a write against a closed database succeeded")
	}
	if err := db.DismissReminder(ctx, user.ID, domain.ReminderEmptyDay, "2026-03-16"); err == nil {
		t.Error("dismissing a reminder against a closed database succeeded")
	}
	if _, err := db.ReindexSearch(ctx); err == nil {
		t.Error("reindexing a closed database succeeded")
	}

	// Closing an already-closed store is not an error, because the cleanup path
	// and an explicit shutdown both run.
	if err := db.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
}

// TestAPanicInATransactionRollsBackBeforeItEscapes.
//
// There is one write connection. A panic that escaped with the transaction
// still open would leave it holding a lock, and the next write would block for
// ever - a crash in one request turning into a hung process. The rollback in
// the deferred recover is what prevents that, and it has to re-panic rather
// than swallow the bug.
func TestAPanicInATransactionRollsBackBeforeItEscapes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Error("the panic was swallowed by InTx")
			}
		}()
		_ = db.InTx(ctx, func(tx *sql.Tx) error {
			if _, err := CreateCustomerTx(ctx, tx, domain.Customer{
				Name: "Half written", Currency: "EUR",
			}); err != nil {
				t.Errorf("write inside the transaction: %v", err)
			}
			panic("something went wrong half way through")
		})
	}()

	// The connection is usable, which is the whole point.
	if _, err := db.CreateCustomer(ctx, domain.Customer{Name: "Afterwards", Currency: "EUR"}); err != nil {
		t.Fatalf("the write connection is still locked after a panic: %v", err)
	}

	// And the panicking transaction left nothing behind.
	customers, err := db.ListCustomers(ctx, UnrestrictedScope(), true)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	for _, customer := range customers {
		if customer.Name == "Half written" {
			t.Error("a panicking transaction's write was committed")
		}
	}
}

// TestAFailedTransactionLeavesNothingBehind.
//
// The ordinary error path of the same mechanism, and the reason the service
// layer can write a change and its audit row in one call: if either fails,
// neither happened.
func TestAFailedTransactionLeavesNothingBehind(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	wanted := errors.New("the second write failed")
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := CreateCustomerTx(ctx, tx, domain.Customer{
			Name: "Rolled back", Currency: "EUR",
		}); err != nil {
			return err
		}
		return wanted
	})
	if !errors.Is(err, wanted) {
		t.Fatalf("InTx returned %v, want the body's own error unwrapped", err)
	}

	customers, err := db.ListCustomers(ctx, UnrestrictedScope(), true)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	for _, customer := range customers {
		if customer.Name == "Rolled back" {
			t.Error("a rolled-back write is in the database")
		}
	}
}
