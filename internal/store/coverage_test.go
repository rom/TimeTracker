package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The store's reads and writes that the service tests exercise only in passing.
//
// A service test proves the rule the service applies; it says nothing about
// which rows the SQL underneath actually matched. These ask the store directly:
// what a filter includes and excludes, what an update leaves alone, what a scope
// refuses, and what a listing's order is - all of which are properties of the
// query rather than of the code around it.

// entryAt records one entry and returns it.
func entryAt(t *testing.T, db *DB, user domain.User, assignment domain.Assignment,
	at time.Time, seconds int64, status domain.EntryStatus) domain.TimeEntry {
	t.Helper()

	end := at.Add(time.Duration(seconds) * time.Second)
	entry, err := db.CreateEntry(context.Background(), domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: at, EndedAt: &end, DurationSeconds: seconds,
		BillableSeconds: seconds, Status: status, Billable: true, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	return entry
}

// TestUpdatingAnEntryChangesOnlyWhatWasAsked.
//
// The update writes every editable column, so a field the caller did not change
// is a field the statement rewrites with the value it was handed. That is fine
// when the caller loaded the row first and wrong when they built a struct from a
// form - and the difference is invisible until a correction silently clears a
// note somebody else wrote.
func TestUpdatingAnEntryChangesOnlyWhatWasAsked(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	at := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entry := entryAt(t, db, user, assignment, at, 3600, domain.StatusConfirmed)

	loaded, err := db.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	loaded.Note = "corrected"
	loaded.DurationSeconds = 7200
	if err := db.UpdateEntry(ctx, loaded); err != nil {
		t.Fatalf("update: %v", err)
	}

	after, err := db.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.Note != "corrected" || after.DurationSeconds != 7200 {
		t.Errorf("the correction did not save: %+v", after)
	}
	if after.AssignmentID != assignment.ID || after.UserID != user.ID {
		t.Errorf("the update moved the entry: %+v", after)
	}
	if !after.StartedAt.Equal(at) {
		t.Errorf("the update moved the start time to %v", after.StartedAt)
	}
	if after.Status != domain.StatusConfirmed {
		t.Errorf("the update changed the status to %q", after.Status)
	}

	// An update of a row that is not there has to fail rather than report
	// success over nothing: a correction to a deleted entry is a mistake the
	// caller needs to hear about.
	missing := loaded
	missing.ID = 9999
	if err := db.UpdateEntry(ctx, missing); err == nil {
		t.Error("updating an entry that does not exist succeeded")
	}
}

// TestDeletingAnEntry.
func TestDeletingAnEntry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	entry := entryAt(t, db, user, assignment,
		time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), 3600, domain.StatusConfirmed)

	if err := db.DeleteEntry(ctx, entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetEntry(ctx, entry.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted entry is still readable: %v", err)
	}
	if err := db.DeleteEntry(ctx, entry.ID); err == nil {
		t.Error("deleting an entry twice succeeded the second time")
	}
}

// TestCountingEntriesMatchesTheListing.
//
// The count and the rows come from one set of conditions on purpose: a total
// that disagrees with the rows under it is worse than no total. This is the test
// that keeps them built from the same place.
func TestCountingEntriesMatchesTheListing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		status := domain.StatusConfirmed
		if i == 4 {
			status = domain.StatusPending
		}
		entryAt(t, db, user, assignment, start.AddDate(0, 0, i), 3600, status)
	}

	for _, filter := range []EntryFilter{
		{UserID: user.ID, Scope: UnrestrictedScope()},
		{UserID: user.ID, Scope: UnrestrictedScope(), CountingOnly: true},
		{UserID: user.ID, Scope: UnrestrictedScope(), From: start, To: start.AddDate(0, 0, 2)},
	} {
		entries, err := db.ListEntries(ctx, filter)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		count, err := db.CountEntries(ctx, filter)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != len(entries) {
			t.Errorf("count = %d but the listing has %d rows (filter %+v)",
				count, len(entries), filter)
		}
	}

	// A page is a window on the same count, not a different one: the count has
	// to be the whole filter, or "1-50 of 50" appears on page one of three.
	paged := EntryFilter{UserID: user.ID, Scope: UnrestrictedScope(), Limit: 2}
	entries, err := db.ListEntries(ctx, paged)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	count, err := db.CountEntries(ctx, paged)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if len(entries) != 2 || count != 5 {
		t.Errorf("a page of 2 from 5 reported %d rows and a count of %d", len(entries), count)
	}
}

// TestStreamingAnEntryRangeMatchesListingIt.
//
// EachEntry is what an export runs on, and it must cover exactly what the
// listing covers: an export that holds more or fewer entries than the screen it
// was taken from is a wrong invoice.
//
// The *order* is deliberately not the same. The listing is newest-first because
// that is what a screen wants; the stream is oldest-first because
// domain.UnionAccumulator can only total overlapping intervals correctly when
// they arrive ascending. Both are asserted here, because the second one is the
// kind of thing an innocent-looking ORDER BY tidy-up silently breaks.
func TestStreamingAnEntryRangeMatchesListingIt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		entryAt(t, db, user, assignment, start.AddDate(0, 0, i), 3600, domain.StatusConfirmed)
	}

	filter := EntryFilter{UserID: user.ID, Scope: UnrestrictedScope()}
	listed, err := db.ListEntries(ctx, filter)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	var streamed []domain.TimeEntry
	for entry, err := range db.EachEntry(ctx, filter) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		streamed = append(streamed, entry)
	}

	if len(streamed) != len(listed) {
		t.Fatalf("the stream yielded %d entries, the listing %d", len(streamed), len(listed))
	}

	// The same set, whatever order each puts it in.
	inListing := map[int64]bool{}
	for _, entry := range listed {
		inListing[entry.ID] = true
	}
	for _, entry := range streamed {
		if !inListing[entry.ID] {
			t.Errorf("entry %d is in the export and not on the screen it came from", entry.ID)
		}
		delete(inListing, entry.ID)
	}
	for id := range inListing {
		t.Errorf("entry %d is on the screen and missing from the export", id)
	}

	// The stream ascends, which is what the elapsed-time total depends on.
	for i := 1; i < len(streamed); i++ {
		if streamed[i].StartedAt.Before(streamed[i-1].StartedAt) {
			t.Fatalf("the stream went backwards at %d (%s after %s)",
				i, streamed[i].StartedAt, streamed[i-1].StartedAt)
		}
	}

	// The listing descends, which is what a screen shows.
	for i := 1; i < len(listed); i++ {
		if listed[i].StartedAt.After(listed[i-1].StartedAt) {
			t.Fatalf("the listing went forwards at %d (%s after %s)",
				i, listed[i].StartedAt, listed[i-1].StartedAt)
		}
	}

	// A limit truncates the stream from the oldest end, since that is the end it
	// starts at.
	capped := filter
	capped.Limit = 5
	var first []int64
	for entry, err := range db.EachEntry(ctx, capped) {
		if err != nil {
			t.Fatalf("stream a limited range: %v", err)
		}
		first = append(first, entry.ID)
	}
	if len(first) != 5 {
		t.Errorf("a limit of 5 yielded %d entries", len(first))
	}
	for i, id := range first {
		if id != streamed[i].ID {
			t.Errorf("limited stream at %d = %d, want %d - a limit must not reorder",
				i, id, streamed[i].ID)
		}
	}

	// Abandoning the stream early stops it, which is what a cancelled download
	// does. The test is that it returns at all.
	seen := 0
	for range db.EachEntry(ctx, filter) {
		seen++
		if seen == 3 {
			break
		}
	}
	if seen != 3 {
		t.Errorf("a consumer that stopped after 3 saw %d", seen)
	}
}

// TestTheInboxBadgeCountsWhatIsWaiting.
//
// It appears on every page, so it is a query that runs constantly and a number
// people trust without checking. It counts entries and expenses awaiting *this*
// person's decision, and nothing else.
func TestTheInboxBadgeCountsWhatIsWaiting(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	other := seedUser(t, db, "Somebody Else")

	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entryAt(t, db, user, assignment, start, 3600, domain.StatusConfirmed)
	entryAt(t, db, user, assignment, start.AddDate(0, 0, 1), 3600, domain.StatusPending)
	entryAt(t, db, user, assignment, start.AddDate(0, 0, 2), 3600, domain.StatusPending)
	entryAt(t, db, other, assignment, start, 3600, domain.StatusPending)

	count, err := db.CountPending(ctx, user.ID, UnrestrictedScope())
	if err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if count != 2 {
		t.Errorf("the badge says %d, want this person's 2 proposals", count)
	}

	// An expense awaiting the same person adds to it: the badge is one number
	// for one inbox, and a split count would make somebody check two screens.
	if _, err := db.CreateExpense(ctx, domain.Expense{
		UserID: user.ID, EnteredBy: other.ID, ProjectID: assignment.ProjectID,
		SpentOn: "2026-03-16", AmountMinor: 42500, Currency: "SEK",
		Description: "Taxi", Status: domain.StatusPending,
	}); err != nil {
		t.Fatalf("create expense: %v", err)
	}
	if count, err = db.CountPending(ctx, user.ID, UnrestrictedScope()); err != nil || count != 3 {
		t.Errorf("after a pending expense the badge says %d (err %v), want 3", count, err)
	}
}

// TestExpensesAreFilteredTheWayTheScreenAsks.
func TestExpensesAreFilteredTheWayTheScreenAsks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	other := seedUser(t, db, "Somebody Else")

	for _, expense := range []domain.Expense{
		{UserID: user.ID, ProjectID: assignment.ProjectID, SpentOn: "2026-03-10",
			AmountMinor: 10000, Currency: "SEK", Description: "Taxi", Status: domain.StatusConfirmed},
		{UserID: user.ID, ProjectID: assignment.ProjectID, SpentOn: "2026-03-20",
			AmountMinor: 20000, Currency: "SEK", Description: "Hotel", Status: domain.StatusConfirmed},
		{UserID: user.ID, ProjectID: assignment.ProjectID, SpentOn: "2026-04-01",
			AmountMinor: 30000, Currency: "SEK", Description: "Flight", Status: domain.StatusPending},
		{UserID: other.ID, ProjectID: assignment.ProjectID, SpentOn: "2026-03-15",
			AmountMinor: 40000, Currency: "SEK", Description: "Theirs", Status: domain.StatusConfirmed},
	} {
		expense.EnteredBy = expense.UserID
		if _, err := db.CreateExpense(ctx, expense); err != nil {
			t.Fatalf("create expense: %v", err)
		}
	}

	for _, filter := range []struct {
		name string
		f    ExpenseFilter
		want int
	}{
		{"everything in scope", ExpenseFilter{Scope: UnrestrictedScope()}, 4},
		{"one person's", ExpenseFilter{UserID: 1, Scope: UnrestrictedScope()}, 3},
		{"a date range, half open at the end",
			ExpenseFilter{Scope: UnrestrictedScope(), From: "2026-03-01", To: "2026-04-01"}, 3},
		{"one project", ExpenseFilter{Scope: UnrestrictedScope(), ProjectID: 1}, 4},
		{"awaiting a decision", ExpenseFilter{Scope: UnrestrictedScope(),
			Statuses: []domain.EntryStatus{domain.StatusPending}}, 1},
		{"capped", ExpenseFilter{Scope: UnrestrictedScope(), Limit: 2}, 2},
		{"a project nobody has", ExpenseFilter{Scope: UnrestrictedScope(), ProjectID: 99}, 0},
	} {
		expenses, err := db.ListExpenses(ctx, filter.f)
		if err != nil {
			t.Fatalf("%s: %v", filter.name, err)
		}
		if len(expenses) != filter.want {
			t.Errorf("%s: %d expenses, want %d", filter.name, len(expenses), filter.want)
		}
	}

	// Newest first, because an expense screen is read from the top.
	expenses, err := db.ListExpenses(ctx, ExpenseFilter{Scope: UnrestrictedScope()})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if expenses[0].SpentOn != "2026-04-01" {
		t.Errorf("the listing starts at %s, want the newest", expenses[0].SpentOn)
	}
	// And it carries the names a screen needs, so a listing is one query.
	if expenses[0].ProjectName == "" || expenses[0].CustomerName == "" {
		t.Errorf("the listing has no project or customer name: %+v", expenses[0])
	}
}

// TestCorrectingAndWithdrawingAnExpense.
func TestCorrectingAndWithdrawingAnExpense(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	created, err := db.CreateExpense(ctx, domain.Expense{
		UserID: user.ID, EnteredBy: user.ID, ProjectID: assignment.ProjectID,
		SpentOn: "2026-03-16", AmountMinor: 42500, Currency: "SEK",
		Description: "Taxi", Status: domain.StatusConfirmed, Billable: true,
	})
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}

	created.AmountMinor = 54000
	created.Description = "Taxi, corrected"
	if err := db.UpdateExpense(ctx, created); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := db.GetExpense(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.AmountMinor != 54000 || after.Description != "Taxi, corrected" {
		t.Errorf("the correction did not save: %+v", after)
	}
	if !after.Billable || after.Currency != "SEK" {
		t.Errorf("the correction changed something it was not asked to: %+v", after)
	}

	if err := db.DeleteExpense(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetExpense(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a withdrawn expense is still readable: %v", err)
	}
	if err := db.UpdateExpense(ctx, created); err == nil {
		t.Error("correcting a withdrawn expense succeeded")
	}
}

// TestProjectMembershipIsTheScopeTheAuthoriserReads.
//
// Every scoped query in the application is built from these three reads, so a
// mistake here is not one wrong screen but a whole role seeing the wrong set.
func TestProjectMembershipIsTheScopeTheAuthoriserReads(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	other := seedUser(t, db, "Somebody Else")

	second, err := db.CreateProject(ctx, domain.Project{
		CustomerID: 1, Name: "Second", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := db.AddProjectMember(ctx, ProjectMember{
		ProjectID: assignment.ProjectID, UserID: user.ID, RateMinor: 95000,
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if err := db.AddProjectMember(ctx, ProjectMember{
		ProjectID: second.ID, UserID: user.ID,
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}

	member, err := db.IsProjectMember(ctx, user.ID, assignment.ProjectID)
	if err != nil || !member {
		t.Errorf("IsProjectMember = %v, %v for a member", member, err)
	}
	if member, err = db.IsProjectMember(ctx, other.ID, assignment.ProjectID); err != nil || member {
		t.Errorf("IsProjectMember = %v, %v for somebody who is not one", member, err)
	}

	projects, err := db.ProjectIDsForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("project ids: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("the member is scoped to %v, want both projects", projects)
	}
	if none, err := db.ProjectIDsForUser(ctx, other.ID); err != nil || len(none) != 0 {
		t.Errorf("a non-member is scoped to %v (err %v), want nothing", none, err)
	}

	// A member's own rate on a project, which overrides the project's.
	rate, err := db.MemberRateMinor(ctx, user.ID, assignment.ProjectID)
	if err != nil {
		t.Fatalf("member rate: %v", err)
	}
	if rate != 95000 {
		t.Errorf("the member rate is %d, want 95000", rate)
	}
	if rate, err = db.MemberRateMinor(ctx, user.ID, second.ID); err != nil || rate != 0 {
		t.Errorf("a membership with no rate reports %d (err %v), want 0", rate, err)
	}

	listed, err := db.ListProjectMembers(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(listed) != 1 || listed[0].UserName == "" {
		t.Errorf("the membership list is %+v; it needs the name to render", listed)
	}

	// Adding twice updates rather than duplicating: the screen offers "add",
	// and somebody adding a person already there means "set their rate".
	if err := db.AddProjectMember(ctx, ProjectMember{
		ProjectID: assignment.ProjectID, UserID: user.ID, RateMinor: 120000,
	}); err != nil {
		t.Fatalf("re-add member: %v", err)
	}
	listed, err = db.ListProjectMembers(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(listed) != 1 || listed[0].RateMinor != 120000 {
		t.Errorf("adding a member twice gave %+v", listed)
	}

	if err := db.RemoveProjectMember(ctx, assignment.ProjectID, user.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if member, err = db.IsProjectMember(ctx, user.ID, assignment.ProjectID); err != nil || member {
		t.Error("a removed member is still a member")
	}
}

// TestAnIdleObservationIsOneQuestion.
//
// The page reports a stretch when it notices one and again when the timer stops,
// and a laptop woken twice in a lunch hour reports two overlapping stretches of
// the same absence. Merging them is the difference between being asked once and
// being asked three times about the same hour.
func TestAnIdleObservationIsOneQuestion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entry := entryAt(t, db, user, assignment, start, 4*3600, domain.StatusConfirmed)

	first := domain.IdleObservation{
		EntryID: entry.ID, UserID: user.ID,
		StartedAt: start.Add(time.Hour), EndedAt: start.Add(2 * time.Hour),
		Source: domain.IdleUntouched,
	}
	id, err := db.CreateIdleObservation(ctx, first)
	if err != nil {
		t.Fatalf("record an observation: %v", err)
	}

	// An overlapping report finds the one that is already there.
	found, err := db.OverlappingIdleObservation(ctx, entry.ID,
		start.Add(90*time.Minute), start.Add(150*time.Minute))
	if err != nil {
		t.Fatalf("overlap: %v", err)
	}
	if found.ID != id {
		t.Errorf("the overlapping report found %d, want %d", found.ID, id)
	}

	// A stretch somewhere else does not.
	if _, err := db.OverlappingIdleObservation(ctx, entry.ID,
		start.Add(3*time.Hour), start.Add(4*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("a separate stretch matched an existing observation: %v", err)
	}

	// Widening covers both, so the person is asked once about the whole absence.
	if err := db.WidenIdleObservation(ctx, id,
		start.Add(90*time.Minute), start.Add(150*time.Minute)); err != nil {
		t.Fatalf("widen: %v", err)
	}
	widened, err := db.GetIdleObservation(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !widened.EndedAt.Equal(start.Add(150 * time.Minute)) {
		t.Errorf("widening gave %v, want the later end", widened.EndedAt)
	}
	if !widened.StartedAt.Equal(start.Add(time.Hour)) {
		t.Errorf("widening moved the start to %v", widened.StartedAt)
	}

	unresolved, err := db.UnresolvedIdleObservations(ctx, user.ID, 20)
	if err != nil {
		t.Fatalf("unresolved: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0].AssignmentName == "" {
		t.Errorf("the review panel sees %+v; it needs the assignment to render", unresolved)
	}

	// Answered, and gone from the panel.
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return ResolveIdleObservationTx(ctx, tx, id, domain.IdleKeep, time.Now())
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if unresolved, err = db.UnresolvedIdleObservations(ctx, user.ID, 20); err != nil || len(unresolved) != 0 {
		t.Errorf("an answered observation is still being asked about: %+v (err %v)", unresolved, err)
	}
	// Answering it twice is a double-click, not an error to show somebody.
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		return ResolveIdleObservationTx(ctx, tx, id, domain.IdleKeep, time.Now())
	})
	if err == nil {
		t.Error("resolving an answered observation succeeded; the second answer " +
			"would overwrite the first")
	}
}

// TestObservationsOnARunningTimerAreANoticeNotAQuestion.
//
// A resolution rewrites an interval, and a running timer's interval is still
// being measured. So an observation on one is shown as a notice and becomes a
// question when the timer stops - which is a difference in which query finds it.
func TestObservationsOnARunningTimerAreANoticeNotAQuestion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	running, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
		StartedAt: start, Status: domain.StatusConfirmed, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("start a timer: %v", err)
	}
	if _, err := db.CreateIdleObservation(ctx, domain.IdleObservation{
		EntryID: running.ID, UserID: user.ID,
		StartedAt: start.Add(time.Hour), EndedAt: start.Add(2 * time.Hour),
		Source: domain.IdleAsleep,
	}); err != nil {
		t.Fatalf("record an observation: %v", err)
	}

	questions, err := db.UnresolvedIdleObservations(ctx, user.ID, 20)
	if err != nil {
		t.Fatalf("unresolved: %v", err)
	}
	if len(questions) != 0 {
		t.Errorf("a running timer is being asked about: %+v", questions)
	}
	notices, err := db.RunningIdleObservations(ctx, user.ID)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(notices) != 1 {
		t.Errorf("the running timer has %d notices, want 1", len(notices))
	}
}

// TestDismissingAReminderIsScopedToItsDay.
//
// Dismissing today's nudge must not dismiss tomorrow's, or the feature switches
// itself off the first time somebody uses it. The scope is what makes that true,
// so this asserts the scope reaches the row and comes back out of it.
func TestDismissingAReminderIsScopedToItsDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, _ := seed(t, db)

	today := DismissalKey(domain.ReminderEmptyDay, "2026-03-16")
	tomorrow := DismissalKey(domain.ReminderEmptyDay, "2026-03-17")
	otherKind := DismissalKey(domain.ReminderRunningTimers, "2026-03-16")
	for _, pair := range [][2]string{{today, tomorrow}, {today, otherKind}} {
		if pair[0] == pair[1] {
			t.Fatalf("two different reminders share the key %q, so one dismissal silences both", pair[0])
		}
	}

	if err := db.DismissReminder(ctx, user.ID, domain.ReminderEmptyDay, "2026-03-16"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	dismissed, err := db.DismissedReminders(ctx, user.ID, []string{"2026-03-16", "2026-03-17"})
	if err != nil {
		t.Fatalf("read dismissals: %v", err)
	}
	if !dismissed[today] {
		t.Error("the dismissal did not stick")
	}
	if dismissed[tomorrow] {
		t.Error("dismissing today also dismissed tomorrow")
	}
	if dismissed[otherKind] {
		t.Error("dismissing the empty-day nudge also dismissed the running-timer one")
	}

	// Twice is one row, because a double click is not an error.
	if err := db.DismissReminder(ctx, user.ID, domain.ReminderEmptyDay, "2026-03-16"); err != nil {
		t.Errorf("dismissing twice: %v", err)
	}
	if n := countRows(t, db, "reminder_dismissals", "1=1"); n != 1 {
		t.Errorf("%d dismissal rows after dismissing the same nudge twice, want 1", n)
	}

	// Nobody else's screen is affected.
	other, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Someone Else", Role: domain.RoleMember, TimeZone: "UTC",
		Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	theirs, err := db.DismissedReminders(ctx, other.ID, []string{"2026-03-16"})
	if err != nil {
		t.Fatalf("read their dismissals: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("one person's dismissal reached another person's screen: %v", theirs)
	}

	// No scopes is no query, and an empty set rather than a nil map.
	none, err := db.DismissedReminders(ctx, user.ID, nil)
	if err != nil {
		t.Fatalf("read no scopes: %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("asking about no days = %v, want an empty set", none)
	}
}

// TestSettingsRoundTrip.
//
// One row, read on every page render and written by one form. Every field has to
// survive, because a save that silently resets the idle threshold or the week
// start changes behaviour nobody asked to change.
func TestSettingsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	before, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if before.DefaultCurrency == "" {
		t.Error("a fresh instance has no default currency")
	}

	updated := before
	updated.DefaultCurrency = "USD"
	updated.DefaultRounding = "up/900/0"
	updated.DefaultRateMinor = 150000
	updated.WeekStart = 7
	updated.IdleSeconds = 600
	updated.RemindersEnabled = true
	updated.ReminderHour = 17
	updated.ShowClock = true
	updated.NavPosition = "left"
	updated.ClockFormat = "12h"
	updated.DateFormat = "dmy"
	updated.DayStartHour = 7
	updated.DayEndHour = 19
	updated.DayOverflow = "arrows"
	updated.BackupPassword = "a-password"
	if err := db.UpdateSettings(ctx, updated); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	after, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if after != updated {
		t.Errorf("the settings did not round trip:\n got %+v\nwant %+v", after, updated)
	}
}

// TestTheCatalogueListsWhatIsNotArchived.
//
// Archiving is the only removal there is, so every picker in the application
// depends on these listings excluding archived records by default - and on the
// administration screen being able to ask for them anyway.
func TestTheCatalogueListsWhatIsNotArchived(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_, assignment := seed(t, db)

	retired, err := db.CreateAssignment(ctx, domain.Assignment{
		ProjectID: assignment.ProjectID, Name: "Retired", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if err := db.SetAssignmentArchived(ctx, retired.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	active, err := db.ListAssignments(ctx, UnrestrictedScope(), 0, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 1 || active[0].ID != assignment.ID {
		t.Errorf("the picker offers %+v, want only the live assignment", active)
	}

	all, err := db.ListAssignments(ctx, UnrestrictedScope(), 0, true)
	if err != nil {
		t.Fatalf("list including archived: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("the administration screen sees %d assignments, want 2", len(all))
	}

	// Restoring brings it back to the picker.
	if err := db.SetAssignmentArchived(ctx, retired.ID, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if active, err = db.ListAssignments(ctx, UnrestrictedScope(), 0, false); err != nil || len(active) != 2 {
		t.Errorf("a restored assignment is not offered: %d (err %v)", len(active), err)
	}

	// One assignment, with the whole path on it: a picker shows
	// customer/project/assignment, and doing that with three queries per row is
	// how a page gets slow.
	one, err := db.GetAssignment(ctx, assignment.ID)
	if err != nil {
		t.Fatalf("get assignment: %v", err)
	}
	if one.ProjectName == "" || one.CustomerName == "" || one.Currency == "" {
		t.Errorf("the assignment does not carry its path: %+v", one)
	}
	if _, err := db.GetAssignment(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("an assignment that does not exist: %v", err)
	}
}

// TestProjectsCarryTheirCustomerAndBudget.
func TestProjectsCarryTheirCustomerAndBudget(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_, assignment := seed(t, db)

	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	project.BudgetSeconds = 100 * 3600
	project.BudgetMinor = 2000000
	project.RoundingRule = "up/900/0"
	if err := db.UpdateProject(ctx, project); err != nil {
		t.Fatalf("update project: %v", err)
	}

	listed, err := db.ListProjects(ctx, UnrestrictedScope(), 0, false)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("%d projects, want 1", len(listed))
	}
	if listed[0].BudgetSeconds != 100*3600 || listed[0].BudgetMinor != 2000000 {
		t.Errorf("the budget did not survive the listing: %+v", listed[0])
	}
	if listed[0].CustomerName == "" || listed[0].Currency == "" {
		t.Errorf("the project does not carry its customer: %+v", listed[0])
	}

	budgeted, err := db.BudgetedProjects(ctx, UnrestrictedScope())
	if err != nil {
		t.Fatalf("budgeted projects: %v", err)
	}
	if len(budgeted) != 1 {
		t.Errorf("%d projects have budgets, want 1", len(budgeted))
	}

	// A project with no budget is not on the budget report at all, rather than
	// on it as a row of zeroes.
	bare, err := db.CreateProject(ctx, domain.Project{CustomerID: 1, Name: "Unbudgeted"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if budgeted, err = db.BudgetedProjects(ctx, UnrestrictedScope()); err != nil || len(budgeted) != 1 {
		t.Errorf("an unbudgeted project (%d) reached the report: %d rows (err %v)",
			bare.ID, len(budgeted), err)
	}
}
