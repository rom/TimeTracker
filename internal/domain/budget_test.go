package domain

import (
	"testing"
	"time"
)

// Budget consumption and burn.
//
// The consumption half is arithmetic and is tested as such. The projection half
// is a guess, and the tests are mostly about the cases where it must refuse to
// guess - a specific date derived from one week of history is worse than a
// blank cell, because it looks like an answer.

func hours(n int64) int64 { return n * 3600 }

// line builds a project with a budget and some consumption.
func line(budgetHours, usedHours, recentHours int64, activeWeeks int) BudgetLine {
	return BudgetLine{
		ProjectID: 1, ProjectName: "Migration", Currency: "SEK",
		Budget:        Budget{Seconds: hours(budgetHours)},
		UsedSeconds:   hours(usedHours),
		RecentSeconds: hours(recentHours),
		ActiveWeeks:   activeWeeks,
	}
}

func TestBudgetConsumption(t *testing.T) {
	l := line(100, 75, 0, 0)

	if got := l.RemainingSeconds(); got != hours(25) {
		t.Errorf("remaining = %ds, want 25h", got)
	}
	if got := l.UsedPercent(); got != 75 {
		t.Errorf("used = %d%%, want 75", got)
	}
	if l.Overrun() {
		t.Error("75 of 100 hours is not an overrun")
	}
}

// TestBudgetOverrunIsReportedNotClamped.
//
// "Twelve hours over" is the number somebody needs. A percentage capped at 100
// and a remaining figure floored at zero would both hide it, and the row would
// look exactly like a project that finished exactly on budget.
func TestBudgetOverrunIsReportedNotClamped(t *testing.T) {
	l := line(100, 112, 0, 0)

	if !l.Overrun() {
		t.Error("112 of 100 hours is an overrun")
	}
	if got := l.RemainingSeconds(); got != -hours(12) {
		t.Errorf("remaining = %ds, want -12h", got)
	}
	if got := l.UsedPercent(); got != 112 {
		t.Errorf("used = %d%%, want 112 rather than a capped 100", got)
	}
}

// TestUsedFractionTakesTheBindingConstraint.
//
// A project with both budgets runs out at whichever it reaches first, so the
// headline figure has to be the worse of the two. Reporting the hours when the
// money is nearly gone is how a project overruns while its report looks calm.
func TestUsedFractionTakesTheBindingConstraint(t *testing.T) {
	l := BudgetLine{
		Budget:      Budget{Seconds: hours(100), Minor: 100_000},
		UsedSeconds: hours(50), // half the hours
		UsedMinor:   90_000,    // nine tenths of the money
	}
	if got := l.UsedPercent(); got != 90 {
		t.Errorf("used = %d%%, want 90 - the money is the binding constraint", got)
	}
}

// TestProjectBurn.
//
// The arithmetic a reader should be able to check in their head: 40 hours left,
// 10 hours a week, four weeks.
func TestProjectBurn(t *testing.T) {
	now := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	l := line(100, 60, 40, 4) // 40 hours over 4 active weeks = 10/week

	p := ProjectBurn(l, now)
	if !p.Projected() {
		t.Fatalf("expected a projection, got reason %q", p.Reason)
	}
	if p.WeeklySeconds != hours(10) {
		t.Errorf("rate = %ds/week, want 10h", p.WeeklySeconds)
	}
	if p.WeeksLeft != 4 {
		t.Errorf("weeks left = %d, want 4", p.WeeksLeft)
	}
	if want := now.AddDate(0, 0, 28); !p.RunsOut.Equal(want) {
		t.Errorf("runs out %s, want %s",
			p.RunsOut.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

// TestProjectBurnRefusesToGuess.
//
// Four refusals, each of which would otherwise produce a confident date from no
// evidence. The reason is carried rather than left blank, because a blank cell
// reads as zero.
func TestProjectBurnRefusesToGuess(t *testing.T) {
	now := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		line BudgetLine
		want string
	}{
		{
			name: "no budget at all",
			line: BudgetLine{UsedSeconds: hours(40), RecentSeconds: hours(40), ActiveWeeks: 4},
			want: "no_budget",
		},
		{
			// One week of work says nothing about a rate.
			name: "a single week of history",
			line: line(100, 10, 10, 1),
			want: "not_enough_history",
		},
		{
			name: "nothing recorded recently",
			line: line(100, 60, 0, 3),
			want: "no_recent_activity",
		},
		{
			// Projecting when a budget will run out, after it has, is nonsense.
			name: "already over",
			line: line(100, 130, 20, 4),
			want: "already_over",
		},
		{
			// A money budget with only non-billable time against it: hours are
			// being consumed but there is no money rate to divide by.
			name: "consumption in the unit that is not budgeted",
			line: BudgetLine{
				Budget:        Budget{Minor: 100_000},
				UsedSeconds:   hours(40),
				RecentSeconds: hours(40),
				ActiveWeeks:   4,
			},
			want: "no_recent_activity",
		},
	}
	for _, c := range cases {
		got := ProjectBurn(c.line, now)
		if got.Reason != c.want {
			t.Errorf("%s: reason = %q, want %q", c.name, got.Reason, c.want)
		}
		if got.Projected() {
			t.Errorf("%s: should not have projected", c.name)
		}
		if !got.RunsOut.IsZero() {
			t.Errorf("%s: carries a date it should not have", c.name)
		}
	}
}

// TestProjectBurnAveragesOverActiveWeeks.
//
// A fortnight's holiday inside the window must not halve the rate. Averaging
// over the weeks that had work makes the estimate the more pessimistic of the
// two readings, which is the right direction to be wrong in when somebody is
// deciding whether to ask the client for more budget.
func TestProjectBurnAveragesOverActiveWeeks(t *testing.T) {
	now := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)

	// 40 hours in the window, but only two of the four weeks had any work.
	busy := ProjectBurn(line(100, 60, 40, 2), now)
	if busy.WeeklySeconds != hours(20) {
		t.Errorf("rate = %ds/week, want 20h averaged over the two active weeks",
			busy.WeeklySeconds)
	}
	// The same consumption spread over four active weeks lasts twice as long.
	steady := ProjectBurn(line(100, 60, 40, 4), now)
	if steady.WeeksLeft <= busy.WeeksLeft {
		t.Errorf("a steadier project should last longer: %d weeks vs %d",
			steady.WeeksLeft, busy.WeeksLeft)
	}
}

// TestProjectBurnTakesWhicheverRunsOutFirst.
//
// A project stops at the first cap it hits, so the date has to be the earlier
// of the two.
func TestProjectBurnTakesWhicheverRunsOutFirst(t *testing.T) {
	now := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)

	l := BudgetLine{
		Budget:      Budget{Seconds: hours(100), Minor: 100_000},
		UsedSeconds: hours(20), UsedMinor: 80_000,
		// Over four active weeks: 8 hours and 4,000 minor units a week. That
		// leaves 80 hours, which is ten weeks, and 20,000, which is five.
		RecentSeconds: hours(8) * 4, RecentMinor: 4_000 * 4,
		ActiveWeeks: 4,
	}
	p := ProjectBurn(l, now)
	if !p.Projected() {
		t.Fatalf("expected a projection, got %q", p.Reason)
	}
	if p.WeeksLeft != 5 {
		t.Errorf("weeks left = %d, want 5 - the money runs out long before the hours",
			p.WeeksLeft)
	}
}
