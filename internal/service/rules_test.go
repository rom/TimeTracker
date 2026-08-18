package service

import (
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Customer contract rules applied to real entries and expenses.
//
// The domain tests prove the arithmetic. These prove the rules reach it: that a
// kind chosen on a form survives the round trip to the database, that a rate is
// frozen onto the entry as every other rate is, and that a threshold prompts
// rather than reclassifies.

// withRules sets contract terms on the fixture's customer.
func (f *fixture) withRules(t *testing.T, rules domain.RateRules) {
	t.Helper()
	customer, err := f.svc.Customer(f.ctx, 1)
	if err != nil {
		t.Fatalf("load customer: %v", err)
	}
	customer.Rules = rules
	if err := f.svc.UpdateCustomer(f.ctx, customer); err != nil {
		t.Fatalf("save rules: %v", err)
	}
}

// TestKindSurvivesStorage is the regression this feature nearly shipped with:
// the kind was applied when the amount was computed but never written, so the
// rate was right and the reason for it was lost. A stored field that is not
// stored is worse than an absent one, because everything downstream believes it.
func TestKindSurvivesStorage(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{OvertimeMultiplierPct: 150})

	created, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true, Kind: domain.KindOvertime,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Re-read, so this is what the database holds rather than what we passed in.
	stored, err := f.svc.Entry(f.ctx, created.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Kind != domain.KindOvertime {
		t.Errorf("stored kind = %q, want overtime", stored.Kind)
	}

	// And an edit keeps it, which is a separate SQL statement and so a separate
	// chance to drop it.
	updated, err := f.svc.UpdateEntry(f.ctx, created.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 7200, Billable: true, Kind: domain.KindTravel,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Kind != domain.KindTravel {
		t.Errorf("kind after edit = %q, want travel", updated.Kind)
	}
}

// TestOvertimeAndTravelRatesAreApplied, through the service, with the rate
// frozen onto the entry exactly as an ordinary one is.
func TestOvertimeAndTravelRatesAreApplied(t *testing.T) {
	f := newFixture(t)
	// The fixture's customer bills 1250.00/h and the project rounds up to the
	// quarter hour, so a whole hour is a whole hour.
	f.withRules(t, domain.RateRules{
		OvertimeMultiplierPct: 150,
		TravelBilling:         domain.TravelAtRate,
		TravelMultiplierPct:   50,
	})

	kinds := map[domain.EntryKind]int64{
		domain.KindWork:     125000,
		domain.KindOvertime: 187500,
		domain.KindTravel:   62500,
	}
	hour := 0
	for kind, wantRate := range kinds {
		hour++
		entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
			AssignmentID: f.assignment.ID,
			StartedAt:    f.now.Add(time.Duration(hour) * time.Hour),
			// Two hours, so the amount is not accidentally equal to the rate and
			// a bug swapping the two would still show.
			DurationSeconds: 7200, Billable: true, Kind: kind,
		})
		if err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		if entry.RateMinor != wantRate {
			t.Errorf("%s rate = %d, want %d", kind, entry.RateMinor, wantRate)
		}
		if want := wantRate * 2; entry.AmountMinor != want {
			t.Errorf("%s amount = %d, want %d", kind, entry.AmountMinor, want)
		}
	}
}

// TestUnbilledTravelIsRecordedButNotBilled: the time appears in full on the
// timesheet and carries no amount. The person's own billable flag is left
// alone - it is their statement about the work, not the contract's.
func TestUnbilledTravelIsRecordedButNotBilled(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{TravelBilling: domain.TravelUnbilled})

	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 7200, Billable: true, Kind: domain.KindTravel,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if entry.DurationSeconds != 7200 {
		t.Errorf("the travel time was not recorded in full: %d", entry.DurationSeconds)
	}
	if entry.AmountMinor != 0 {
		t.Errorf("unbilled travel carries an amount of %d", entry.AmountMinor)
	}
	if !entry.Billable {
		t.Error("the entry's own billable flag was rewritten by the customer's contract")
	}

	// It still counts as time worked; it is only worth nothing.
	day, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("day view: %v", err)
	}
	if day.Totals.SummedSeconds != 7200 {
		t.Errorf("tracked time = %d, want the travel to be counted", day.Totals.SummedSeconds)
	}
}

// TestOvertimeNoticePromptsRatherThanReclassifies. The notice is the whole
// mechanism: nothing is billed differently until a person says so.
func TestOvertimeNoticePromptsRatherThanReclassifies(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{
		OvertimeMultiplierPct:         150,
		OvertimeDailyThresholdSeconds: 8 * 3600,
	})

	ten, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 10 * 3600, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Nothing was reclassified: it is ten hours of ordinary work at the
	// ordinary rate, because nobody has said otherwise.
	if ten.KindOrDefault() != domain.KindWork {
		t.Errorf("the entry was silently reclassified as %q", ten.Kind)
	}
	if ten.RateMinor != 125000 {
		t.Errorf("rate = %d, want the ordinary rate", ten.RateMinor)
	}

	notices, err := f.svc.OvertimeNotices(f.ctx, f.now)
	if err != nil {
		t.Fatalf("notices: %v", err)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %+v, want one", notices)
	}
	if notices[0].ExcessSeconds() != 2*3600 {
		t.Errorf("excess = %d, want two hours", notices[0].ExcessSeconds())
	}

	// Marking the excess as overtime settles it, and the notice stops. A prompt
	// that keeps firing after it has been dealt with trains people to ignore it.
	if _, err := f.svc.UpdateEntry(f.ctx, ten.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 8 * 3600, Billable: true,
	}); err != nil {
		t.Fatalf("shorten the day: %v", err)
	}
	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now.Add(9 * time.Hour),
		DurationSeconds: 2 * 3600, Billable: true, Kind: domain.KindOvertime,
	}); err != nil {
		t.Fatalf("record the overtime: %v", err)
	}

	if notices, err = f.svc.OvertimeNotices(f.ctx, f.now); err != nil {
		t.Fatalf("notices: %v", err)
	}
	if len(notices) != 0 {
		t.Errorf("the notice survived being dealt with: %+v", notices)
	}
}

// TestNoThresholdMeansNoNotice: a customer with no overtime terms should never
// be nagged about a long day. It is not the tool's business.
func TestNoThresholdMeansNoNotice(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 14 * 3600, Billable: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	notices, err := f.svc.OvertimeNotices(f.ctx, f.now)
	if err != nil {
		t.Fatalf("notices: %v", err)
	}
	if len(notices) != 0 {
		t.Errorf("a customer with no overtime terms produced %+v", notices)
	}
}

// TestMileageIsPricedFromTheCustomersRule. The point is that nobody multiplies
// 42.5 by 25.00 by hand.
func TestMileageIsPricedFromTheCustomersRule(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{MileageRateMinor: 2500, ExpenseMarkupPct: 10})

	expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: f.assignment.ProjectID, SpentOn: "2026-03-16",
		Category: "Travel", Description: "Site visit",
		Quantity: "42.5", Unit: domain.UnitKilometre,
		Billable: true, Reimbursable: true,
	})
	if err != nil {
		t.Fatalf("create expense: %v", err)
	}

	if expense.AmountMinor != 106250 {
		t.Errorf("cost = %d, want 106250 (42.5 x 25.00)", expense.AmountMinor)
	}
	if expense.UnitRateMinor != 2500 {
		t.Errorf("the rate used was not recorded on the claim: %d", expense.UnitRateMinor)
	}
	// Markup applies to the billable side only; what is paid back is the cost.
	if expense.BilledMinor != 116875 {
		t.Errorf("billed = %d, want 116875 (cost plus 10%%)", expense.BilledMinor)
	}
}

// TestExplicitMarkupIsNotOverriddenByTheCustomersDefault: someone who typed 0%
// meant 0%. Inheriting over that would put a margin on a claim deliberately
// made at cost.
func TestExplicitMarkupIsNotOverriddenByTheCustomersDefault(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{ExpenseMarkupPct: 15})

	atCost, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: f.assignment.ProjectID, SpentOn: "2026-03-16",
		Description: "Software licence", Amount: "1000",
		MarkupPercent: 0, MarkupGiven: true, Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if atCost.MarkupPercent != 0 {
		t.Errorf("an explicit 0%% markup became %d%%", atCost.MarkupPercent)
	}

	inherited, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: f.assignment.ProjectID, SpentOn: "2026-03-16",
		Description: "Taxi", Amount: "1000", Billable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inherited.MarkupPercent != 15 {
		t.Errorf("markup = %d, want the customer's 15%%", inherited.MarkupPercent)
	}
}

// TestExpensesNotInvoicedToThisCustomer: reimbursed to whoever paid, never
// billed on.
func TestExpensesNotInvoicedToThisCustomer(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{ExpenseBilling: domain.ExpenseNotBilled})

	expense, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: f.assignment.ProjectID, SpentOn: "2026-03-16",
		Description: "Hotel", Amount: "1500", Billable: true, Reimbursable: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if expense.Billable {
		t.Error("an expense was marked billable for a customer that is never invoiced for them")
	}
	if !expense.Reimbursable {
		t.Error("the claim stopped being reimbursable, which is a different question")
	}
}

// TestReceiptIsRequiredBeforeAWeekCanBeSubmitted. It cannot be enforced at
// creation - an attachment needs an expense to belong to - so this is the first
// point at which it can be, and the last at which it is cheap to fix.
func TestReceiptIsRequiredBeforeAWeekCanBeSubmitted(t *testing.T) {
	f := newFixture(t)
	f.withRules(t, domain.RateRules{ReceiptRequiredAboveMinor: 50000}) // 500.00

	if _, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true,
	}); err != nil {
		t.Fatalf("record time: %v", err)
	}

	// Below the threshold: no evidence needed, and the week submits.
	small, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: f.assignment.ProjectID, SpentOn: f.now.Format("2006-01-02"),
		Description: "Coffee", Amount: "45", Billable: true,
	})
	if err != nil {
		t.Fatalf("create small expense: %v", err)
	}
	if needs, err := f.svc.NeedsReceipt(f.ctx, small); err != nil || needs {
		t.Errorf("a claim under the threshold was asked for a receipt (%v, %v)", needs, err)
	}
	if _, err := f.svc.SubmitWeek(f.ctx, f.now); err != nil {
		t.Fatalf("submit with only a small claim: %v", err)
	}
	if err := f.svc.WithdrawWeek(f.ctx, f.now); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	// Above it, with nothing attached: flagged, and the week is refused.
	large, err := f.svc.CreateExpense(f.ctx, ExpenseInput{
		ProjectID: f.assignment.ProjectID, SpentOn: f.now.Format("2006-01-02"),
		Description: "Flight", Amount: "4500", Billable: true,
	})
	if err != nil {
		t.Fatalf("create large expense: %v", err)
	}
	needs, err := f.svc.NeedsReceipt(f.ctx, large)
	if err != nil {
		t.Fatalf("NeedsReceipt: %v", err)
	}
	if !needs {
		t.Fatal("a claim over the threshold was not flagged for a receipt")
	}

	_, err = f.svc.SubmitWeek(f.ctx, f.now)
	if err == nil {
		t.Fatal("a week was submitted with a claim missing its receipt")
	}
	// The message has to name the claim, or the person has nothing to act on.
	if !strings.Contains(err.Error(), "Flight") {
		t.Errorf("the refusal does not say which claim: %v", err)
	}
}
