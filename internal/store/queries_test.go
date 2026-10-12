package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// The queries behind the reports and the catalogue editors.
//
// These are the reads whose correctness is their SQL: a scope that lets the
// wrong rows through, a filter that narrows to the wrong set, an update that
// writes some columns and forgets others. A mock would assert nothing here, so
// they all run against real SQLite like the rest of the store's tests.

// secondProject adds another customer, project and assignment, so a scope test
// has something to be excluded from.
func secondProject(t *testing.T, db *DB) (domain.Customer, domain.Project, domain.Assignment) {
	t.Helper()
	ctx := context.Background()

	customer, err := db.CreateCustomer(ctx, domain.Customer{Name: "Globex", Currency: "SEK"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	project, err := db.CreateProject(ctx, domain.Project{
		CustomerID: customer.ID, Name: "Rollout", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	assignment, err := db.CreateAssignment(ctx, domain.Assignment{
		ProjectID: project.ID, Name: "Support", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return customer, project, assignment
}

// retag saves an entry together with its tags, the way the service layer does:
// one transaction, because the tag rows and the search index have to land with
// the entry or not at all.
func retag(t *testing.T, db *DB, entry domain.TimeEntry, tags ...string) {
	t.Helper()
	entry.Tags = tags
	err := db.InTx(context.Background(), func(tx *sql.Tx) error {
		return UpdateEntryWithTagsTx(context.Background(), tx, entry)
	})
	if err != nil {
		t.Fatalf("save entry with tags: %v", err)
	}
}

// TestConsumptionCountsWhatABudgetIsSpentOn.
//
// Consumption is the "counting" rule - confirmed and not flagged - and hours
// are hours worked while money is only billable work. Both halves are asserted
// from the same set of entries, because the two sums come out of one query and
// a mistake in either is a wrong figure in front of a customer.
func TestConsumptionCountsWhatABudgetIsSpentOn(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	day := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)

	// Four hours of counting work, of which three are billable.
	counting := entryAt(t, db, user, assignment, day, 4*3600, domain.StatusConfirmed)
	counting.BillableSeconds = 3 * 3600
	if err := db.UpdateEntry(ctx, counting); err != nil {
		t.Fatalf("set billable seconds: %v", err)
	}

	// None of these are consumption.
	entryAt(t, db, user, assignment, day.AddDate(0, 0, 1), 8*3600, domain.StatusPending)
	entryAt(t, db, user, assignment, day.AddDate(0, 0, 2), 8*3600, domain.StatusRejected)
	flagged := entryAt(t, db, user, assignment, day.AddDate(0, 0, 3), 8*3600, domain.StatusConfirmed)
	flagged.Flagged = true
	if err := db.UpdateEntry(ctx, flagged); err != nil {
		t.Fatalf("flag entry: %v", err)
	}

	used, err := db.ConsumptionByProject(ctx, UnrestrictedScope(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("consumption: %v", err)
	}
	got := used[assignment.ProjectID]
	if got.Seconds != 4*3600 {
		t.Errorf("hours consumed = %ds, want 4h - a proposal, a draft and a flagged entry are questions, not consumption",
			got.Seconds)
	}
	if got.FirstEntry.IsZero() || got.LastEntry.IsZero() {
		t.Errorf("consumption carries no dates: %+v", got)
	}

	// A window excludes what falls outside it, so a report "this quarter" is
	// about this quarter.
	empty, err := db.ConsumptionByProject(ctx, UnrestrictedScope(),
		day.AddDate(0, 0, 30), day.AddDate(0, 0, 60))
	if err != nil {
		t.Fatalf("windowed consumption: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("a window with no work in it reported %d projects", len(empty))
	}

	// A scope that names the other project sees none of this one's spending,
	// which is what keeps one customer's budget off another's screen.
	_, other, _ := secondProject(t, db)
	narrowed, err := db.ConsumptionByProject(ctx,
		Scope{ProjectIDs: []int64{other.ID}}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("scoped consumption: %v", err)
	}
	if _, found := narrowed[assignment.ProjectID]; found {
		t.Error("a scope naming one project reported another project's consumption")
	}
}

// TestActiveWeeksCountsWeeksAndNotEntries.
//
// The burn rate divides by this number. Counting entries instead of weeks would
// make a project that logs twice a day look half as busy as it is, and the
// projected run-out date would be twice as far away.
func TestActiveWeeksCountsWeeksAndNotEntries(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	// Three entries across two calendar weeks.
	monday := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entryAt(t, db, user, assignment, monday, 3600, domain.StatusConfirmed)
	entryAt(t, db, user, assignment, monday.AddDate(0, 0, 1), 3600, domain.StatusConfirmed)
	entryAt(t, db, user, assignment, monday.AddDate(0, 0, 7), 3600, domain.StatusConfirmed)

	weeks, err := db.ActiveWeeksByProject(ctx, UnrestrictedScope(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("active weeks: %v", err)
	}
	if weeks[assignment.ProjectID] != 2 {
		t.Errorf("%d active weeks from three entries across two weeks, want 2",
			weeks[assignment.ProjectID])
	}

	// A flagged entry is not activity, for the same reason it is not
	// consumption.
	if _, found := weeks[0]; found {
		t.Error("the map has a zero project id in it")
	}

	// And the window applies here too.
	recent, err := db.ActiveWeeksByProject(ctx, UnrestrictedScope(),
		monday.AddDate(0, 0, 7), time.Time{})
	if err != nil {
		t.Fatalf("windowed active weeks: %v", err)
	}
	if recent[assignment.ProjectID] != 1 {
		t.Errorf("%d active weeks in the last week, want 1", recent[assignment.ProjectID])
	}
}

// TestAScopeWithNoProjectsSeesNothing.
//
// The condition for "a member of no projects" must be false rather than empty.
// An empty fragment would read as no restriction at all, which is how a
// permission check becomes a permission grant.
func TestAScopeWithNoProjectsSeesNothing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	entryAt(t, db, user, assignment, time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		3600, domain.StatusConfirmed)

	nothing := Scope{}
	entries, err := db.ListEntries(ctx, EntryFilter{Scope: nothing})
	if err != nil {
		t.Fatalf("list with an empty scope: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a scope with no projects and no customer returned %d entries - "+
			"an empty scope must mean nothing, not everything", len(entries))
	}

	used, err := db.ConsumptionByProject(ctx, nothing, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("consumption with an empty scope: %v", err)
	}
	if len(used) != 0 {
		t.Errorf("an empty scope saw %d projects' consumption", len(used))
	}

	// A customer scope sees that customer's work and no other's.
	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	_, _, otherAssignment := secondProject(t, db)
	entryAt(t, db, user, otherAssignment, time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		3600, domain.StatusConfirmed)

	client := Scope{CustomerID: project.CustomerID}
	entries, err = db.ListEntries(ctx, EntryFilter{Scope: client})
	if err != nil {
		t.Fatalf("list as a client: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("a client of one customer saw %d entries, want only their own 1", len(entries))
	}
}

// TestEntryFiltersNarrowToWhatTheScreenAsked.
//
// Every one of these is a control on a screen. They are asserted together
// because they share one condition builder, and a filter that quietly stops
// narrowing shows somebody more than they asked for without ever erroring.
func TestEntryFiltersNarrowToWhatTheScreenAsked(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	_, otherProject, otherAssignment := secondProject(t, db)

	day := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	work := entryAt(t, db, user, assignment, day, 3600, domain.StatusConfirmed)
	travel := entryAt(t, db, user, assignment, day.AddDate(0, 0, 1), 3600, domain.StatusConfirmed)
	elsewhere := entryAt(t, db, user, otherAssignment, day.AddDate(0, 0, 2), 3600, domain.StatusRejected)

	// Give one entry a kind, a note and a tag to filter on.
	travel.Kind = domain.KindTravel
	travel.Note = "Train to the client site"
	retag(t, db, travel, "onsite")
	// And one that is recorded but not billable. It goes through the same
	// tag-aware write, because that is what maintains the search index and the
	// free-text cases below read it.
	elsewhere.Billable = false
	retag(t, db, elsewhere)

	all := EntryFilter{Scope: UnrestrictedScope()}
	ids := func(t *testing.T, f EntryFilter) []int64 {
		t.Helper()
		entries, err := db.ListEntries(ctx, f)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := make([]int64, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.ID)
		}
		return out
	}

	cases := []struct {
		name   string
		filter func(EntryFilter) EntryFilter
		want   []int64
	}{
		{"one assignment", func(f EntryFilter) EntryFilter {
			f.AssignmentID = otherAssignment.ID
			return f
		}, []int64{elsewhere.ID}},
		{"one project", func(f EntryFilter) EntryFilter {
			f.ProjectID = otherProject.ID
			return f
		}, []int64{elsewhere.ID}},
		{"one customer", func(f EntryFilter) EntryFilter {
			f.CustomerID = otherProject.CustomerID
			return f
		}, []int64{elsewhere.ID}},
		{"a status", func(f EntryFilter) EntryFilter {
			f.Statuses = []domain.EntryStatus{domain.StatusRejected}
			return f
		}, []int64{elsewhere.ID}},
		{"only what counts", func(f EntryFilter) EntryFilter {
			f.CountingOnly = true
			return f
		}, []int64{travel.ID, work.ID}},
		{"only billable", func(f EntryFilter) EntryFilter {
			f.BillableOnly = true
			return f
		}, []int64{travel.ID, work.ID}},
		{"a kind", func(f EntryFilter) EntryFilter {
			f.Kinds = []domain.EntryKind{domain.KindTravel}
			return f
		}, []int64{travel.ID}},
		{"a tag", func(f EntryFilter) EntryFilter {
			f.Tags = []string{"onsite"}
			return f
		}, []int64{travel.ID}},
		{"free text in the note", func(f EntryFilter) EntryFilter {
			f.Query = "client site"
			return f
		}, []int64{travel.ID}},
		{"free text in the customer name", func(f EntryFilter) EntryFilter {
			f.Query = "Globex"
			return f
		}, []int64{elsewhere.ID}},
		{"a date range", func(f EntryFilter) EntryFilter {
			f.From = day
			f.To = day.AddDate(0, 0, 1)
			return f
		}, []int64{work.ID}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(t, tc.filter(all))
			if len(got) != len(tc.want) {
				t.Fatalf("filtering by %s matched %v, want %v", tc.name, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("filtering by %s matched %v, want %v", tc.name, got, tc.want)
				}
			}
		})
	}

	// A regular expression is a different matcher, not a different set of
	// columns: it searches what the substring search searches.
	regexp := all
	regexp.Query = "Tr[a]in"
	regexp.UseRegexp = true
	if got := ids(t, regexp); len(got) != 1 || got[0] != travel.ID {
		t.Errorf("a regular expression matched %v, want the travel entry", got)
	}

	// An invalid one is the user's mistake, reported rather than crashed on.
	broken := all
	broken.Query = "("
	broken.UseRegexp = true
	if _, err := db.ListEntries(ctx, broken); err == nil {
		t.Error("an unparseable regular expression was accepted")
	}

	// Two tags mean both, not either.
	both := all
	both.Tags = []string{"onsite", "never-used"}
	if got := ids(t, both); len(got) != 0 {
		t.Errorf("asking for two tags matched %v, want only entries carrying both", got)
	}
}

// TestProposalsTheAuthorMadeForSomebodyElse.
//
// A proposal that disappears from its author's view is worse than one that is
// refused, so this listing is how they follow it up. Their own entries are not
// proposals and must not appear.
func TestProposalsTheAuthorMadeForSomebodyElse(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	author, assignment := seed(t, db)

	colleague, err := db.CreateUser(ctx, domain.User{
		DisplayName: "Colleague", Role: domain.RoleMember, TimeZone: "UTC",
		Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	day := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	own := entryAt(t, db, author, assignment, day, 3600, domain.StatusConfirmed)

	end := day.AddDate(0, 0, 1).Add(time.Hour)
	proposal, err := db.CreateEntry(ctx, domain.TimeEntry{
		UserID: colleague.ID, EnteredBy: author.ID, AssignmentID: assignment.ID,
		StartedAt: day.AddDate(0, 0, 1), EndedAt: &end, DurationSeconds: 3600,
		Status: domain.StatusPending, Billable: true, TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("create proposal: %v", err)
	}

	listed, err := db.ListEntriesEnteredBy(ctx, author.ID, UnrestrictedScope())
	if err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("%d proposals, want 1 - the author's own entries are not proposals", len(listed))
	}
	if listed[0].ID != proposal.ID {
		t.Errorf("listed entry %d, want the proposal %d (own entry is %d)",
			listed[0].ID, proposal.ID, own.ID)
	}

	// The scope still applies: a proposal onto a project the author can no
	// longer see is not shown.
	none, err := db.ListEntriesEnteredBy(ctx, author.ID, Scope{})
	if err != nil {
		t.Fatalf("scoped proposals: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an empty scope listed %d proposals", len(none))
	}
}

// TestTheApprovalQueueIsScopedThroughTheWorkItContains.
//
// A manager approves the weeks that include work on their projects. The scope
// goes through the entries rather than the period row, because a period has no
// project of its own - and an empty submitted week is deliberately not in the
// queue, since there is nothing in it to approve.
func TestTheApprovalQueueIsScopedThroughTheWorkItContains(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)
	_, otherProject, _ := secondProject(t, db)

	const weekStart = "2026-03-16"
	entryAt(t, db, user, assignment, time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		4*3600, domain.StatusConfirmed)

	if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
		UserID: user.ID, WeekStart: weekStart, Status: domain.PeriodSubmitted,
		SubmittedAt: time.Now(), SubmittedSeconds: 4 * 3600,
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	queue, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted, UnrestrictedScope())
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(queue) != 1 || queue[0].WeekStart != weekStart {
		t.Fatalf("the administrator's queue = %+v, want the one submitted week", queue)
	}
	if queue[0].UserName == "" {
		t.Error("the queue does not say whose week it is")
	}
	if queue[0].SubmittedSeconds != 4*3600 {
		t.Errorf("the queue says %ds were submitted, want 4h - an approval is a "+
			"decision about specific figures", queue[0].SubmittedSeconds)
	}

	// The week's total as it stands now is a separate read, because it is what
	// the service compares against the submitted figure to notice a week that
	// changed after it was submitted.
	now, err := db.WeekSeconds(ctx, user.ID, weekStart)
	if err != nil {
		t.Fatalf("week seconds: %v", err)
	}
	if now != 4*3600 {
		t.Errorf("the week now totals %ds, want 4h", now)
	}

	// A manager on the project sees it.
	mine, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted,
		Scope{ProjectIDs: []int64{assignment.ProjectID}})
	if err != nil {
		t.Fatalf("scoped queue: %v", err)
	}
	if len(mine) != 1 {
		t.Errorf("a manager of the project saw %d submissions, want 1", len(mine))
	}

	// A manager of a different project does not.
	theirs, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted,
		Scope{ProjectIDs: []int64{otherProject.ID}})
	if err != nil {
		t.Fatalf("other scoped queue: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("a manager of an unrelated project saw %d submissions: %+v", len(theirs), theirs)
	}

	// And somebody with no projects at all sees nothing, without a query.
	if nobody, err := db.ListPeriodsByStatus(ctx, domain.PeriodSubmitted, Scope{}); err != nil {
		t.Fatalf("empty scoped queue: %v", err)
	} else if len(nobody) != 0 {
		t.Errorf("an empty scope saw %d submissions", len(nobody))
	}

	// A different status is a different queue.
	approved, err := db.ListPeriodsByStatus(ctx, domain.PeriodApproved, UnrestrictedScope())
	if err != nil {
		t.Fatalf("approved queue: %v", err)
	}
	if len(approved) != 0 {
		t.Errorf("%d approved weeks, want none", len(approved))
	}
}

// TestPeriodsInRangeShowWhoHasNotSubmitted.
//
// The compliance report is about absence: a week with no row is a week nobody
// submitted, which is exactly what it exists to show. So this read must return
// the rows that exist in the range and nothing outside it, and the caller works
// out the gaps.
func TestPeriodsInRangeShowWhoHasNotSubmitted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	entryAt(t, db, user, assignment, time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC),
		3600, domain.StatusConfirmed)

	for _, week := range []string{"2026-03-09", "2026-03-16", "2026-03-23"} {
		if err := db.UpsertPeriod(ctx, domain.TimesheetPeriod{
			UserID: user.ID, WeekStart: week, Status: domain.PeriodSubmitted,
			SubmittedAt: time.Now(),
		}); err != nil {
			t.Fatalf("submit %s: %v", week, err)
		}
	}

	inRange, err := db.ListPeriodsInRange(ctx, "2026-03-16", "2026-03-23", UnrestrictedScope())
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(inRange) != 1 || inRange[0].WeekStart != "2026-03-16" {
		t.Fatalf("a half-open range of one week returned %+v, want only 2026-03-16", inRange)
	}

	wide, err := db.ListPeriodsInRange(ctx, "2026-03-01", "2026-04-01", UnrestrictedScope())
	if err != nil {
		t.Fatalf("wide range: %v", err)
	}
	if len(wide) != 3 {
		t.Errorf("%d weeks in March, want 3", len(wide))
	}

	scoped, err := db.ListPeriodsInRange(ctx, "2026-03-01", "2026-04-01",
		Scope{ProjectIDs: []int64{assignment.ProjectID}})
	if err != nil {
		t.Fatalf("scoped range: %v", err)
	}
	if len(scoped) != 1 || scoped[0].WeekStart != "2026-03-16" {
		t.Errorf("scoped to the project with work in it returned %+v, want the week that work is in", scoped)
	}

	if nobody, err := db.ListPeriodsInRange(ctx, "2026-03-01", "2026-04-01", Scope{}); err != nil {
		t.Fatalf("empty scoped range: %v", err)
	} else if len(nobody) != 0 {
		t.Errorf("an empty scope saw %d weeks", len(nobody))
	}
}

// TestDatedTermsRoundTripAndResolve.
//
// Terms are a contract. Every rule has to survive the write, the later edit and
// the read, because one that silently reverts to zero is an invoice at the wrong
// rate - and the resolution has to pick the row in force on the day rather than
// the newest row.
func TestDatedTermsRoundTripAndResolve(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_, assignment := seed(t, db)

	project, err := db.GetProject(ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	rules := domain.RateRules{
		OvertimeRateMinor:              12_000,
		OvertimeMultiplierPct:          150,
		OvertimeDailyThresholdSeconds:  8 * 3600,
		OvertimeWeeklyThresholdSeconds: 40 * 3600,
		TravelBilling:                  domain.TravelAtRate,
		TravelRateMinor:                5_000,
		TravelMultiplierPct:            50,
		ExpenseMarkupPct:               10,
		ExpenseBilling:                 domain.ExpenseBillable,
		MileageRateMinor:               250,
		PerDiemMinor:                   30_000,
		ReceiptRequiredAboveMinor:      100_000,
	}

	created, err := db.CreateContractTerms(ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2026-01-01", Rules: rules, Note: "Signed contract",
	})
	if err != nil {
		t.Fatalf("create terms: %v", err)
	}

	read, err := db.GetContractTerms(ctx, created.ID)
	if err != nil {
		t.Fatalf("read terms: %v", err)
	}
	if read.Rules != rules {
		t.Errorf("terms came back changed:\n got %+v\nwant %+v", read.Rules, rules)
	}
	if read.Note != "Signed contract" || read.EffectiveFrom != "2026-01-01" {
		t.Errorf("terms lost their note or date: %+v", read)
	}

	// An edit writes every rule, not the ones that happen to differ.
	read.Rules.OvertimeMultiplierPct = 200
	read.Rules.PerDiemMinor = 0
	read.Note = "Renewal"
	if err := db.UpdateContractTerms(ctx, read); err != nil {
		t.Fatalf("update terms: %v", err)
	}
	after, err := db.GetContractTerms(ctx, created.ID)
	if err != nil {
		t.Fatalf("re-read terms: %v", err)
	}
	if after.Rules.OvertimeMultiplierPct != 200 || after.Rules.PerDiemMinor != 0 {
		t.Errorf("the edit did not stick: %+v", after.Rules)
	}
	if after.Note != "Renewal" {
		t.Errorf("note = %q, want Renewal", after.Note)
	}
	if after.Rules.MileageRateMinor != 250 {
		t.Errorf("the edit reset a rule nobody touched: mileage = %d", after.Rules.MileageRateMinor)
	}

	// A project's terms override its customer's, field by field, on the day.
	if _, err := db.CreateContractTerms(ctx, domain.ContractTerms{
		Scope: domain.TermsForProject, ScopeID: project.ID,
		EffectiveFrom: "2026-06-01",
		Rules:         domain.RateRules{ExpenseMarkupPct: 25},
	}); err != nil {
		t.Fatalf("create project terms: %v", err)
	}

	before, err := db.ResolveTerms(ctx, project.CustomerID, project.ID, "2026-05-31")
	if err != nil {
		t.Fatalf("resolve before: %v", err)
	}
	if before.ExpenseMarkupPct != 10 {
		t.Errorf("markup on 31 May = %d%%, want the customer's 10%% - "+
			"terms effective in June must not apply in May", before.ExpenseMarkupPct)
	}

	during, err := db.ResolveTerms(ctx, project.CustomerID, project.ID, "2026-06-01")
	if err != nil {
		t.Fatalf("resolve during: %v", err)
	}
	if during.ExpenseMarkupPct != 25 {
		t.Errorf("markup on 1 June = %d%%, want the project's 25%%", during.ExpenseMarkupPct)
	}
	if during.OvertimeMultiplierPct != 200 {
		t.Errorf("the project's terms dropped the customer's overtime rule: %d%%",
			during.OvertimeMultiplierPct)
	}

	// Listing is newest-first, which is the order the editing screen shows.
	listed, err := db.ListContractTerms(ctx, domain.TermsForCustomer, project.CustomerID)
	if err != nil {
		t.Fatalf("list terms: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("%d sets of customer terms, want 1", len(listed))
	}

	// Deleting one leaves the other, and the deleted set is gone.
	if err := db.DeleteContractTerms(ctx, created.ID); err != nil {
		t.Fatalf("delete terms: %v", err)
	}
	if _, err := db.GetContractTerms(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading deleted terms = %v, want not found", err)
	}
	if gone, err := db.ResolveTerms(ctx, project.CustomerID, project.ID, "2026-06-01"); err != nil {
		t.Fatalf("resolve after delete: %v", err)
	} else if gone.OvertimeMultiplierPct != 0 {
		t.Errorf("the customer's deleted overtime rule still applies: %d%%",
			gone.OvertimeMultiplierPct)
	}
}

// TestRoutinesRoundTripAndDelete.
//
// A routine is a template somebody fills a day from. Its weekdays and tags are
// the awkward part - both are lists in a single row - and a routine that comes
// back with the wrong days fills the wrong days.
func TestRoutinesRoundTripAndDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	created, err := db.CreateRoutine(ctx, domain.Routine{
		UserID: user.ID, AssignmentID: assignment.ID, Name: "Standup",
		Note: "Daily standup", Weekdays: []int{1, 2, 3, 4, 5},
		StartTime: "09:00", DurationSeconds: 900, Billable: true,
		Kind: domain.KindWork, Tags: []string{"meeting"}, Active: true,
	})
	if err != nil {
		t.Fatalf("create routine: %v", err)
	}

	read, err := db.GetRoutine(ctx, created.ID)
	if err != nil {
		t.Fatalf("read routine: %v", err)
	}
	if len(read.Weekdays) != 5 || read.Weekdays[0] != 1 || read.Weekdays[4] != 5 {
		t.Errorf("weekdays = %v, want Monday to Friday", read.Weekdays)
	}
	if len(read.Tags) != 1 || read.Tags[0] != "meeting" {
		t.Errorf("tags = %v, want [meeting]", read.Tags)
	}
	if read.StartTime != "09:00" || read.DurationSeconds != 900 || !read.Billable {
		t.Errorf("routine came back changed: %+v", read)
	}
	if read.AssignmentName == "" || read.ProjectName == "" || read.CustomerName == "" {
		t.Errorf("routine carries no names for display: %+v", read)
	}

	// An edit, including emptying a list.
	read.Name = "Standup (short)"
	read.Weekdays = []int{1}
	read.Tags = nil
	read.Active = false
	if err := db.UpdateRoutine(ctx, read); err != nil {
		t.Fatalf("update routine: %v", err)
	}
	after, err := db.GetRoutine(ctx, created.ID)
	if err != nil {
		t.Fatalf("re-read routine: %v", err)
	}
	if len(after.Weekdays) != 1 || len(after.Tags) != 0 || after.Active {
		t.Errorf("the edit did not stick: %+v", after)
	}

	// Inactive routines are hidden from the day screen and visible in settings.
	active, err := db.ListRoutines(ctx, user.ID, true)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("%d active routines after deactivating the only one", len(active))
	}
	all, err := db.ListRoutines(ctx, user.ID, false)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("%d routines in settings, want 1", len(all))
	}

	if err := db.DeleteRoutine(ctx, created.ID); err != nil {
		t.Fatalf("delete routine: %v", err)
	}
	if _, err := db.GetRoutine(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading a deleted routine = %v, want not found", err)
	}
}

// TestQuickStartOffersFavouritesAndRecentWork.
//
// The buttons at the top of the day screen. Frequency before recency is the
// point: a list that reshuffles after every entry is one nobody can build
// muscle memory for, and an assignment nobody has touched in months should drop
// off without anybody archiving it.
func TestQuickStartOffersFavouritesAndRecentWork(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, often := seed(t, db)
	_, _, once := secondProject(t, db)

	day := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		entryAt(t, db, user, often, day.AddDate(0, 0, i), 3600, domain.StatusConfirmed)
	}
	entryAt(t, db, user, once, day, 3600, domain.StatusConfirmed)

	since := day.AddDate(0, 0, -7)
	offered, err := db.QuickStartAssignments(ctx, user.ID, since, 0)
	if err != nil {
		t.Fatalf("quick start: %v", err)
	}
	if len(offered) != 2 {
		t.Fatalf("%d quick-start assignments, want the 2 worked on", len(offered))
	}
	if offered[0].ID != often.ID {
		t.Errorf("the most-used assignment is at position %d, not first", indexOf(offered, often.ID))
	}

	// A favourite comes first whether or not it has been used.
	_, _, favourite := secondProject(t, db)
	favourite.Favourite = true
	if err := db.UpdateAssignment(ctx, favourite); err != nil {
		t.Fatalf("favourite: %v", err)
	}
	offered, err = db.QuickStartAssignments(ctx, user.ID, since, 0)
	if err != nil {
		t.Fatalf("quick start with a favourite: %v", err)
	}
	if len(offered) != 3 || offered[0].ID != favourite.ID {
		t.Errorf("a favourite that has never been used is at position %d of %d, want first",
			indexOf(offered, favourite.ID), len(offered))
	}

	// Work older than the window drops off, leaving only the favourite.
	recent, err := db.QuickStartAssignments(ctx, user.ID, day.AddDate(0, 0, 30), 0)
	if err != nil {
		t.Fatalf("quick start with a recent window: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != favourite.ID {
		t.Errorf("a window with no work in it offered %d assignments, want only the favourite", len(recent))
	}

	// The limit caps the row of buttons.
	capped, err := db.QuickStartAssignments(ctx, user.ID, since, 2)
	if err != nil {
		t.Fatalf("quick start with a limit: %v", err)
	}
	if len(capped) != 2 {
		t.Errorf("a limit of 2 offered %d assignments", len(capped))
	}

	// An archived assignment is not offered, however often it was used.
	if err := db.SetAssignmentArchived(ctx, often.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	after, err := db.QuickStartAssignments(ctx, user.ID, since, 0)
	if err != nil {
		t.Fatalf("quick start after archiving: %v", err)
	}
	if indexOf(after, often.ID) != -1 {
		t.Error("an archived assignment is still offered as a quick start")
	}
}

// indexOf is where in the offered list an assignment landed, or -1.
func indexOf(assignments []domain.Assignment, id int64) int {
	for i, a := range assignments {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// TestRenamingATagRenamesItEverywhere.
//
// A tag is one row referenced by many entries, so a rename is one write. The
// test is that the entries follow it: a rename that left the old name on
// existing entries would split one tag into two.
func TestRenamingATagRenamesItEverywhere(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	day := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entry := entryAt(t, db, user, assignment, day, 3600, domain.StatusConfirmed)
	retag(t, db, entry, "incident")

	tags, err := db.ListTags(ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("%d tags, want 1", len(tags))
	}
	if tags[0].EntryCount != 1 {
		t.Errorf("entry count = %d, want 1", tags[0].EntryCount)
	}

	tag := tags[0]
	tag.Name = "outage"
	tag.ColourKey = "red"
	if err := db.UpdateTag(ctx, tag); err != nil {
		t.Fatalf("rename tag: %v", err)
	}

	byEntry, err := db.TagsForEntries(ctx, []int64{entry.ID})
	if err != nil {
		t.Fatalf("tags for entries: %v", err)
	}
	if got := byEntry[entry.ID]; len(got) != 1 || got[0] != "outage" {
		t.Errorf("the entry still carries %v after the rename, want [outage]", got)
	}

	renamed, err := db.ListTags(ctx)
	if err != nil {
		t.Fatalf("re-list tags: %v", err)
	}
	if len(renamed) != 1 || renamed[0].ColourKey != "red" {
		t.Errorf("the rename produced %+v, want one red tag", renamed)
	}

	// Deleting the tag leaves the entry, without its tag.
	if err := db.DeleteTag(ctx, tag.ID); err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	if _, err := db.GetEntry(ctx, entry.ID); err != nil {
		t.Errorf("deleting a tag took its entry with it: %v", err)
	}
	after, err := db.TagsForEntries(ctx, []int64{entry.ID})
	if err != nil {
		t.Fatalf("tags after delete: %v", err)
	}
	if len(after[entry.ID]) != 0 {
		t.Errorf("the entry still carries %v after the tag was deleted", after[entry.ID])
	}

	// Asking about no entries is no query.
	if none, err := db.TagsForEntries(ctx, nil); err != nil {
		t.Fatalf("tags for no entries: %v", err)
	} else if len(none) != 0 {
		t.Errorf("asking about no entries returned %v", none)
	}
}

// TestRenamingAnAssignmentKeepsItsEntries.
//
// The catalogue editor's write. An update that dropped a column would silently
// change how time is billed, and one that moved an assignment between projects
// would move its history with it.
func TestRenamingAnAssignmentKeepsItsEntries(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	day := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	entry := entryAt(t, db, user, assignment, day, 3600, domain.StatusConfirmed)

	assignment.Name = "Development (phase 2)"
	assignment.Code = "DEV2"
	assignment.ColourKey = "blue"
	assignment.Icon = "wrench"
	assignment.BillableDefault = false
	assignment.RateMinor = 15_000
	assignment.SortOrder = 3
	assignment.Favourite = true
	if err := db.UpdateAssignment(ctx, assignment); err != nil {
		t.Fatalf("update assignment: %v", err)
	}

	read, err := db.GetAssignment(ctx, assignment.ID)
	if err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if read.Name != "Development (phase 2)" || read.Code != "DEV2" ||
		read.ColourKey != "blue" || read.Icon != "wrench" ||
		read.BillableDefault || read.RateMinor != 15_000 ||
		read.SortOrder != 3 || !read.Favourite {
		t.Errorf("the edit did not round trip: %+v", read)
	}
	if read.ProjectID != assignment.ProjectID {
		t.Errorf("project = %d, want %d", read.ProjectID, assignment.ProjectID)
	}

	kept, err := db.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if kept.AssignmentID != assignment.ID {
		t.Error("renaming an assignment moved its entry")
	}
	if kept.AssignmentName != "Development (phase 2)" {
		t.Errorf("the entry still shows %q, want the new name", kept.AssignmentName)
	}
}
