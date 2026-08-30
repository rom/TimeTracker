package domain

import "time"

// Budget consumption and burn.
//
// A budget is a cap somebody agreed with a client: so many hours, or so much
// money, or both. Consumption is arithmetic. The projection is not - it is a
// guess about the future, and the whole difficulty of this feature is saying so
// without making it useless (ADR-0035).

// BurnWindowWeeks is how much recent history a burn rate is averaged over.
//
// Four weeks: long enough that one quiet week does not halve the estimate, short
// enough to notice a project that has just picked up. Averaging over the whole
// life of a project is worse, not better - a project that ran at two hours a
// week for a year and forty a week this month would report a rate nobody
// recognises.
const BurnWindowWeeks = 4

// MinimumBurnWeeks is how many weeks of actual activity a projection requires.
//
// One week of work says nothing about a rate: dividing a remaining budget by a
// single week's total produces a confident-looking date from one data point.
// Below this the report says it cannot project rather than projecting badly.
const MinimumBurnWeeks = 2

// Budget is what was agreed, in whichever of the two units were set.
type Budget struct {
	Seconds int64
	Minor   int64
}

// Set reports whether there is anything to report against.
func (b Budget) Set() bool { return b.Seconds > 0 || b.Minor > 0 }

// BudgetLine is one project's consumption, with the projection where one can
// honestly be made.
type BudgetLine struct {
	ProjectID    int64
	ProjectName  string
	CustomerID   int64
	CustomerName string
	ColourKey    string
	Currency     string
	Archived     bool

	Budget Budget
	// UsedSeconds is time that counts: confirmed, unflagged, and billable or not
	// - hours against an hours budget are hours somebody worked, whoever pays
	// for them. UsedMinor is what has been billed, which only billable work
	// contributes to.
	UsedSeconds int64
	UsedMinor   int64

	// RecentSeconds and RecentMinor are what was consumed inside the burn
	// window, and ActiveWeeks how many weeks of it had any activity at all.
	RecentSeconds int64
	RecentMinor   int64
	ActiveWeeks   int

	// FirstEntry and LastEntry bound what has actually been recorded, so a
	// reader can see how old the numbers are.
	FirstEntry time.Time
	LastEntry  time.Time
}

// UsedFraction is consumption as a fraction of the budget, in whichever unit is
// set - the larger of the two when both are, since the binding constraint is
// whichever runs out first.
//
// Returns 0 when no budget is set. May exceed 1: a project over its budget is
// exactly what this exists to show, and clamping it would hide the number that
// matters.
func (l BudgetLine) UsedFraction() float64 {
	var worst float64
	if l.Budget.Seconds > 0 {
		worst = float64(l.UsedSeconds) / float64(l.Budget.Seconds)
	}
	if l.Budget.Minor > 0 {
		if money := float64(l.UsedMinor) / float64(l.Budget.Minor); money > worst {
			worst = money
		}
	}
	return worst
}

// UsedPercent is UsedFraction as a whole-number percentage, for display.
func (l BudgetLine) UsedPercent() int { return int(l.UsedFraction()*100 + 0.5) }

// RemainingSeconds and RemainingMinor are what is left, or negative when the
// budget has been passed. Negative rather than clamped: "12 hours over" is the
// thing somebody needs to know.
func (l BudgetLine) RemainingSeconds() int64 { return l.Budget.Seconds - l.UsedSeconds }

// RemainingMinor is the money half of RemainingSeconds, in minor units.
func (l BudgetLine) RemainingMinor() int64 { return l.Budget.Minor - l.UsedMinor }

// Overrun reports whether either unit has been passed.
func (l BudgetLine) Overrun() bool {
	return (l.Budget.Seconds > 0 && l.UsedSeconds > l.Budget.Seconds) ||
		(l.Budget.Minor > 0 && l.UsedMinor > l.Budget.Minor)
}

// BurnProjection is an estimate of when a budget runs out, or the reason there
// is no estimate.
//
// The reason is a field rather than an absence because "we cannot say" is
// information, and a blank cell reads as zero.
type BurnProjection struct {
	// WeeklySeconds and WeeklyMinor are the average consumption per week across
	// the burn window's *active* weeks.
	WeeklySeconds int64
	WeeklyMinor   int64
	// WeeksLeft is how long the remaining budget lasts at that rate, rounded
	// down, and RunsOut the date that falls on. Both zero when Reason is set.
	WeeksLeft int
	RunsOut   time.Time
	// Reason is empty when the projection stands, and otherwise says why there
	// is none: "no_budget", "not_enough_history", "no_recent_activity" or
	// "already_over".
	Reason string
}

// Projected reports whether there is a date to show.
func (p BurnProjection) Projected() bool { return p.Reason == "" }

// ProjectBurn estimates when a budget runs out at the recent rate.
//
// Deliberately arithmetic on a stated window rather than a model: the value of
// this number is that a reader can check it in their head, and a projection
// nobody can check is one nobody should act on. It refuses in three cases -
// nothing to project against, not enough history to divide by, and nothing
// recorded recently - because each of those would otherwise produce a specific
// date from no evidence, which is worse than a blank.
func ProjectBurn(line BudgetLine, now time.Time) BurnProjection {
	if !line.Budget.Set() {
		return BurnProjection{Reason: "no_budget"}
	}
	if line.Overrun() {
		// Projecting when a budget will run out, after it has, is nonsense. The
		// overrun itself is the report.
		return BurnProjection{Reason: "already_over"}
	}
	// Order matters here, because both can be true and only one of them is worth
	// reading. A project with nothing at all in the window is quiet, which is a
	// specific and useful thing to say; "too little history" is what to say
	// about a project that *is* being worked on and has not been for long
	// enough to divide by.
	if line.RecentSeconds <= 0 && line.RecentMinor <= 0 {
		return BurnProjection{Reason: "no_recent_activity"}
	}
	if line.ActiveWeeks < MinimumBurnWeeks {
		return BurnProjection{Reason: "not_enough_history"}
	}

	// Averaged over the weeks that had activity rather than over the window, so
	// a fortnight's holiday inside the window does not halve the rate. It makes
	// the estimate the more pessimistic of the two readings, which is the right
	// direction to be wrong in when the number is used to decide whether to ask
	// for more budget.
	weeks := int64(line.ActiveWeeks)
	projection := BurnProjection{
		WeeklySeconds: line.RecentSeconds / weeks,
		WeeklyMinor:   line.RecentMinor / weeks,
	}

	// Whichever unit runs out first is the answer, since the project stops at
	// the first cap it hits.
	weeksLeft := -1
	if line.Budget.Seconds > 0 && projection.WeeklySeconds > 0 {
		weeksLeft = int(line.RemainingSeconds() / projection.WeeklySeconds)
	}
	if line.Budget.Minor > 0 && projection.WeeklyMinor > 0 {
		if byMoney := int(line.RemainingMinor() / projection.WeeklyMinor); weeksLeft < 0 || byMoney < weeksLeft {
			weeksLeft = byMoney
		}
	}
	if weeksLeft < 0 {
		// A budget in one unit and consumption only in the other: hours
		// recorded against a money budget with nothing billable in them. There
		// is no rate to divide by.
		return BurnProjection{Reason: "no_recent_activity"}
	}

	projection.WeeksLeft = weeksLeft
	projection.RunsOut = now.AddDate(0, 0, 7*weeksLeft)
	return projection
}
