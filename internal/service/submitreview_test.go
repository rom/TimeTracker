package service

import (
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Reviewing a week before submitting it.
//
// The review's whole value is that it agrees with the submission. One that said
// "ready" and was then refused would be worse than no review at all - somebody
// would learn to ignore it, which is the state the feature exists to improve
// on. So the central test here walks both paths over the same week and requires
// them to agree, for every shape of week.

// codes lists a review's note codes, for an assertion that does not depend on
// prose.
func codes(notes []SubmitNote) []string {
	var out []string
	for _, note := range notes {
		out = append(out, note.Code)
	}
	return out
}

func hasCode(notes []SubmitNote, want string) bool {
	for _, note := range notes {
		if note.Code == want {
			return true
		}
	}
	return false
}

// TestTheReviewAndTheSubmissionAgree.
//
// Over every shape of week this application can produce. If the review says it
// is ready, submitting must succeed; if it names a blocker, submitting must
// fail. Nothing else is asserted here, because nothing else matters as much:
// the two answers coming apart is the one defect that makes the feature
// counterproductive.
func TestTheReviewAndTheSubmissionAgree(t *testing.T) {
	cases := map[string]func(t *testing.T, f *fixture){
		"a week with nothing in it": func(*testing.T, *fixture) {},
		"an ordinary full week": func(t *testing.T, f *fixture) {
			for day := 0; day < 5; day++ {
				mustCreate(t, f, at(f.now.AddDate(0, 0, day), 9, 0), 8*3600)
			}
		},
		"one day only": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 3600)
		},
		"a week with a gap in it": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 8*3600)
			mustCreate(t, f, at(f.now.AddDate(0, 0, 4), 9, 0), 8*3600)
		},
		"a week with a flagged entry": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 3600)
			flagged := mustCreate(t, f, at(f.now, 14, 0), 40*3600)
			entry, err := f.db.GetEntry(f.ctx, flagged.ID)
			if err != nil {
				t.Fatalf("read entry: %v", err)
			}
			entry.Flagged = true
			if err := f.db.UpdateEntry(f.ctx, entry); err != nil {
				t.Fatalf("flag: %v", err)
			}
		},
		"a week with a timer still running": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 3600)
			if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "still going"); err != nil {
				t.Fatalf("start: %v", err)
			}
		},
		"a week holding only a running timer": func(t *testing.T, f *fixture) {
			if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "still going"); err != nil {
				t.Fatalf("start: %v", err)
			}
		},
		"a week with an expense in it": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 3600)
			project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
			if err != nil {
				t.Fatalf("read project: %v", err)
			}
			if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
				ProjectID: project.ID, SpentOn: f.now.Format("2006-01-02"),
				Category: "travel", Description: "Taxi", Amount: "100", Billable: true,
			}); err != nil {
				t.Fatalf("record an expense: %v", err)
			}
		},
		"a week with an expense missing its receipt": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 3600)
			project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
			if err != nil {
				t.Fatalf("read project: %v", err)
			}
			if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
				Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
				EffectiveFrom: "2020-01-01",
				Rules:         domain.RateRules{ReceiptRequiredAboveMinor: 5000},
			}); err != nil {
				t.Fatalf("save terms: %v", err)
			}
			if _, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
				ProjectID: project.ID, SpentOn: f.now.Format("2006-01-02"),
				Category: "travel", Description: "Flight", Amount: "500", Billable: true,
			}); err != nil {
				t.Fatalf("record a large expense: %v", err)
			}
		},
		"a week already submitted": func(t *testing.T, f *fixture) {
			mustCreate(t, f, at(f.now, 9, 0), 3600)
			if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
				t.Fatalf("submit: %v", err)
			}
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			build(t, f)

			review, err := f.svc.ReviewSubmission(f.ctx, f.now)
			if err != nil {
				t.Fatalf("review: %v", err)
			}

			_, submitErr := f.svc.SubmitWeek(f.ctx, f.now)

			switch {
			case review.Ready() && submitErr != nil:
				t.Errorf("the review said this week was ready and submitting it "+
					"failed with %v; somebody told it was ready and then refused "+
					"learns to ignore the review", submitErr)
			case !review.Ready() && submitErr == nil:
				t.Errorf("the review said this week was not ready (blockers %v, "+
					"CanSubmit %v) and submitting it succeeded; a review that "+
					"blocks what the application accepts is an obstacle",
					codes(review.Blockers), review.Period.CanSubmit)
			}

			// A week that cannot be submitted says why, rather than leaving
			// somebody to work out which control is missing.
			if !review.Ready() && len(review.Blockers) == 0 && review.Period.CanSubmit {
				t.Error("the week is not ready and no blocker says why")
			}
		})
	}
}

// TestTheReviewNamesWhatIsWrongWithAWeek.
//
// Each of these is something somebody would want to see before submitting
// rather than after. They are warnings rather than blockers: an empty Friday is
// somebody's half-day as often as it is an omission, and a review that refused
// to let them past it is a review they learn to click through.
func TestTheReviewNamesWhatIsWrongWithAWeek(t *testing.T) {
	f := newFixture(t)

	// Monday and Tuesday worked, Wednesday to Friday empty.
	mustCreate(t, f, at(f.now, 9, 0), 8*3600)
	mustCreate(t, f, at(f.now.AddDate(0, 0, 1), 9, 0), 8*3600)

	review, err := f.svc.ReviewSubmission(f.ctx, f.now)
	if err != nil {
		t.Fatalf("review: %v", err)
	}

	if !review.Ready() {
		t.Fatalf("a week with two days on it is not submittable: %v",
			codes(review.Blockers))
	}
	if !review.Any() {
		t.Error("a week with three empty weekdays has nothing to say about it")
	}

	empty := 0
	for _, note := range review.Warnings {
		if note.Code == SubmitWarnEmptyDay {
			empty++
			if note.Day.IsZero() {
				t.Error("an empty-day warning does not say which day")
			}
			if weekday := note.Day.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
				t.Errorf("a quiet %s was reported as an omission", weekday)
			}
		}
	}
	if empty != 3 {
		t.Errorf("%d empty weekdays reported, want Wednesday, Thursday and Friday "+
			"(warnings: %v)", empty, codes(review.Warnings))
	}

	// A full working week has nothing to say.
	full := newFixture(t)
	for day := 0; day < 5; day++ {
		mustCreate(t, full, at(full.now.AddDate(0, 0, day), 9, 0), 8*3600)
	}
	complete, err := full.svc.ReviewSubmission(full.ctx, full.now)
	if err != nil {
		t.Fatalf("review a full week: %v", err)
	}
	if complete.Any() {
		t.Errorf("a full working week produced %v and %v",
			codes(complete.Blockers), codes(complete.Warnings))
	}
	if !complete.Ready() {
		t.Error("a full working week is not submittable")
	}
}

// TestTheReviewCountsWhatWillNotBeSubmitted.
//
// A flagged entry counts towards nothing until somebody resolves it, a running
// timer has no length, and a proposal is not this person's to submit. All three
// are in the week and none is in the figure a manager will approve, so a week
// whose total looks right can be short by whatever is in here - which is
// exactly what somebody needs to know before pressing the button.
func TestTheReviewCountsWhatWillNotBeSubmitted(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	theirs := f.asUser(colleague)

	// Four hours that count.
	end := monday.Add(13 * time.Hour)
	if _, err := f.svc.CreateEntry(theirs, EntryInput{
		AssignmentID: assignment.ID, StartedAt: monday.Add(9 * time.Hour),
		EndedAt: &end, Billable: true,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// A proposal from somebody else, which they have not decided about.
	proposalEnd := monday.Add(16 * time.Hour)
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: assignment.ID, StartedAt: monday.Add(14 * time.Hour),
		EndedAt: &proposalEnd, OnBehalfOf: colleague.ID,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}

	// A timer still running.
	if _, err := f.svc.StartTimer(theirs, assignment.ID, "still going"); err != nil {
		t.Fatalf("start: %v", err)
	}

	review, err := f.svc.ReviewSubmission(theirs, monday)
	if err != nil {
		t.Fatalf("review: %v", err)
	}

	if len(review.Pending) != 1 {
		t.Errorf("%d proposals awaiting a decision, want 1", len(review.Pending))
	}
	if len(review.Running) != 1 {
		t.Errorf("%d running timers, want 1", len(review.Running))
	}
	for _, want := range []string{SubmitWarnPending, SubmitWarnRunning} {
		if !hasCode(review.Warnings, want) {
			t.Errorf("the review does not mention %q; its warnings are %v",
				want, codes(review.Warnings))
		}
	}

	// The figure that will be submitted is the one that counts, which is the
	// four hours and not the ten that are in the week.
	if review.Week.Totals.SummedSeconds != 4*3600 {
		t.Errorf("the week totals %ds, want the 4 hours that count",
			review.Week.Totals.SummedSeconds)
	}

	// And it still submits: none of these is a blocker.
	if !review.Ready() {
		t.Errorf("a week with a proposal and a running timer in it cannot be "+
			"submitted: %v", codes(review.Blockers))
	}
}

// TestTheReviewAsksForAMissingReceiptBeforeTheButton.
//
// A customer that requires receipts above an amount requires them before the
// claim is made. SubmitWeek enforces that, and by the time it fires the person
// has already pressed submit - so the review asks first, and points at the
// expense rather than at the week.
func TestTheReviewAsksForAMissingReceiptBeforeTheButton(t *testing.T) {
	f := withBlobs(t, newFixture(t))
	mustCreate(t, f, at(f.now, 9, 0), 8*3600)

	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if err := f.svc.SaveContractTerms(f.ctx, domain.ContractTerms{
		Scope: domain.TermsForCustomer, ScopeID: project.CustomerID,
		EffectiveFrom: "2020-01-01",
		Rules:         domain.RateRules{ReceiptRequiredAboveMinor: 5000},
	}); err != nil {
		t.Fatalf("save terms: %v", err)
	}

	small, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: f.now.Format("2006-01-02"),
		Category: "travel", Description: "Bus fare", Amount: "10", Billable: true,
	})
	if err != nil {
		t.Fatalf("record a small expense: %v", err)
	}
	large, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: project.ID, SpentOn: f.now.Format("2006-01-02"),
		Category: "travel", Description: "Flight", Amount: "500", Billable: true,
	})
	if err != nil {
		t.Fatalf("record a large expense: %v", err)
	}

	review, err := f.svc.ReviewSubmission(f.ctx, f.now)
	if err != nil {
		t.Fatalf("review: %v", err)
	}

	if review.Ready() {
		t.Error("a week with an expense missing a required receipt was reported " +
			"as ready, and submitting it would be refused")
	}
	if !hasCode(review.Blockers, SubmitBlockedReceipts) {
		t.Errorf("the blockers are %v, and none is about receipts",
			codes(review.Blockers))
	}
	if review.MissingReceipts() != 1 {
		t.Errorf("%d expenses are missing receipts, want 1", review.MissingReceipts())
	}
	// It points at the row, not at the week.
	if !review.NeedsReceipt[large.ID] {
		t.Error("the review does not name the expense that needs a receipt")
	}
	if review.NeedsReceipt[small.ID] {
		t.Error("the review asks for a receipt for an expense under the threshold")
	}

	// The expenses themselves are listed, because a claim nobody looked at is
	// the one that comes back.
	if len(review.Expenses) != 2 {
		t.Errorf("%d expenses listed, want both", len(review.Expenses))
	}

	// Attaching the receipt clears the blocker, and the week then submits -
	// which is the agreement this whole feature rests on.
	if _, err := f.svc.Attach(f.ctx, domain.AttachmentOwnerExpense, large.ID,
		"boarding-pass.txt", strings.NewReader("a boarding pass")); err != nil {
		t.Fatalf("attach: %v", err)
	}
	after, err := f.svc.ReviewSubmission(f.ctx, f.now)
	if err != nil {
		t.Fatalf("re-review: %v", err)
	}
	if !after.Ready() {
		t.Fatalf("the week is still not ready after the receipt was attached: %v",
			codes(after.Blockers))
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Errorf("the review said ready and the submission failed: %v", err)
	}
}

// TestReviewingAWeekThatIsAlreadyDecided.
//
// Somebody following a stale link, or pressing back. The review has to say
// which state the week is in rather than offering a button that cannot work.
func TestReviewingAWeekThatIsAlreadyDecided(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, at(f.now, 9, 0), 8*3600)

	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit: %v", err)
	}

	review, err := f.svc.ReviewSubmission(f.ctx, f.now)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if review.Ready() {
		t.Error("a submitted week was reported as ready to submit")
	}
	if !hasCode(review.Blockers, SubmitBlockedLocked) {
		t.Errorf("the blockers are %v, and none says the week is already decided",
			codes(review.Blockers))
	}
	// The state is carried, so the screen can say which it is rather than
	// saying only that something is wrong.
	if review.Period.Period.Status != domain.PeriodSubmitted {
		t.Errorf("the review reports the week as %q", review.Period.Period.Status)
	}
	// Withdrawing is what there is to offer instead.
	if !review.Period.CanWithdraw {
		t.Error("a submitted week offers no way to take it back")
	}

	// Withdrawing makes it ready again.
	if err := f.svc.WithdrawWeek(f.ctx, f.now); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	again, err := f.svc.ReviewSubmission(f.ctx, f.now)
	if err != nil {
		t.Fatalf("re-review: %v", err)
	}
	if !again.Ready() {
		t.Errorf("a withdrawn week is still not ready: %v", codes(again.Blockers))
	}
}

// TestAnEmptyWeekSaysSoRatherThanOfferingToSubmitNothing.
//
// Submitting an empty week would put an item in a manager's queue with nothing
// in it to look at, so it is refused - and the review says which of the two
// reasons applies, since "nothing recorded" and "already submitted" need
// different things done about them.
func TestAnEmptyWeekSaysSoRatherThanOfferingToSubmitNothing(t *testing.T) {
	f := newFixture(t)

	review, err := f.svc.ReviewSubmission(f.ctx, f.now)
	if err != nil {
		t.Fatalf("review an empty week: %v", err)
	}
	if review.Ready() {
		t.Error("an empty week was reported as ready to submit")
	}
	if !hasCode(review.Blockers, SubmitBlockedEmpty) {
		t.Errorf("the blockers are %v, and none says the week is empty",
			codes(review.Blockers))
	}
	// Not also reported as locked: an empty week is open, it is just empty.
	if hasCode(review.Blockers, SubmitBlockedLocked) {
		t.Error("an empty week was also reported as already decided")
	}
	// And no empty-day warnings on top: the week being empty is the one thing
	// to say about it, not five.
	if len(review.Warnings) > 0 {
		t.Errorf("an empty week produced %v on top of being empty, which buries "+
			"the one thing worth saying", codes(review.Warnings))
	}
}
