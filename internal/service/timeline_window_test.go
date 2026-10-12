package service

import (
	"strings"
	"testing"

	"github.com/rom/timetracker/internal/domain"
)

// The day pane's window, and the work that falls outside it.
//
// The pane draws a fixed range of hours, because a pane covering midnight to
// midnight is mostly empty and makes a working day a thin band in the middle.
// The cost is that some work falls outside it, and the whole design rests on
// saying so: a block clamped to the top edge claims a start time the entry
// does not have, which is a lie the pane would tell every morning.

// dayPane renders a day with the window and overflow rule set.
func dayPane(t *testing.T, f *fixture, startHour, endHour int, overflow domain.DayOverflow) DayView {
	t.Helper()
	if err := f.svc.UpdateSettings(f.ctx, SettingsInput{
		DefaultCurrency: "SEK", WeekStart: 1,
		DayStartHour: startHour, DayEndHour: endHour,
		DayOverflow: string(overflow),
	}); err != nil {
		t.Fatalf("set the day window: %v", err)
	}
	view, err := f.svc.Day(f.ctx, f.now)
	if err != nil {
		t.Fatalf("read the day: %v", err)
	}
	return view
}

// TestWorkBeforeThePaneBeginsIsCountedRatherThanClamped.
//
// An early start, with the pane set to begin at eight. Drawing it at eight
// would say it began at eight. So it is counted, and the hour it really began
// is reported, which is what the arrow at the top of the pane says.
func TestWorkBeforeThePaneBeginsIsCountedRatherThanClamped(t *testing.T) {
	f := newFixture(t)

	// Two hours from five, which ends an hour before an eight-o'clock pane
	// begins. Not six until eight: slots are quarter hours and the slot a
	// block lands in is rounded to the nearest, so work ending exactly as the
	// pane opens leaves a sliver inside it and counts as straddling instead.
	mustCreate(t, f, at(f.now, 5, 0), 2*3600)

	view := dayPane(t, f, 8, 18, domain.DayOverflowArrows)
	if !view.Earlier.Any() {
		t.Fatal("work before the pane began was not reported at all, so the pane " +
			"shows an empty morning for somebody who started at six")
	}
	if view.Earlier.Count != 1 {
		t.Errorf("%d blocks reported above the pane, want 1", view.Earlier.Count)
	}
	if view.Earlier.Seconds != 2*3600 {
		t.Errorf("%ds reported above the pane, want the two hours", view.Earlier.Seconds)
	}
	if view.Earlier.FirstHour != 5 {
		t.Errorf("the pane says the work above it began at %02d:00, want 05:00",
			view.Earlier.FirstHour)
	}
	if view.Later.Any() {
		t.Errorf("work before the pane was reported as being after it: %+v", view.Later)
	}

	// The day's total is unaffected: what is off-screen is still worked.
	if view.Totals.SummedSeconds != 2*3600 {
		t.Errorf("the day totals %ds, want the two hours whether or not they are "+
			"drawn", view.Totals.SummedSeconds)
	}
}

// TestWorkAfterThePaneEndsIsCountedToo.
//
// The same at the other end, where it matters more: an evening left running
// is the commonest thing to miss.
func TestWorkAfterThePaneEndsIsCountedToo(t *testing.T) {
	f := newFixture(t)

	mustCreate(t, f, at(f.now, 20, 0), 2*3600)

	view := dayPane(t, f, 8, 18, domain.DayOverflowArrows)
	if !view.Later.Any() {
		t.Fatal("work after the pane ended was not reported")
	}
	if view.Later.Count != 1 || view.Later.Seconds != 2*3600 {
		t.Errorf("the pane reports %d blocks and %ds after it, want 1 and two hours",
			view.Later.Count, view.Later.Seconds)
	}
	if view.Earlier.Any() {
		t.Errorf("work after the pane was reported as being before it: %+v", view.Earlier)
	}
}

// TestWorkStraddlingThePanesEdge.
//
// A block that starts before the window and ends inside it. The visible part
// is drawn, and the hour it really began is still reported - both, because
// either alone misleads: no block at all hides work that is partly on screen,
// and a block with no note of its real start claims the wrong time.
func TestWorkStraddlingThePanesEdge(t *testing.T) {
	f := newFixture(t)

	// Six until ten, against a pane that begins at eight.
	mustCreate(t, f, at(f.now, 6, 0), 4*3600)

	view := dayPane(t, f, 8, 18, domain.DayOverflowArrows)
	if !view.Earlier.Any() {
		t.Fatal("a block straddling the top edge reported nothing above the pane")
	}
	if view.Earlier.FirstHour != 6 {
		t.Errorf("the pane says the work began at %02d:00, want 06:00 - a block "+
			"that appears to start at eight when it started at six is a lie the "+
			"pane would tell every morning", view.Earlier.FirstHour)
	}
	// The figure is slot-aligned rather than exact: what is reported above the
	// pane is the number of quarter-hour slots the block reaches above it, so
	// six until eight against an eight-o'clock pane reads as 1h45m rather than
	// 2h. That is the arrow's job - "there is more up there, from six" - and
	// not an invoice figure; the day's own total is exact.
	if view.Earlier.Seconds != 6300 {
		t.Errorf("%ds reported above the pane; the slots above an eight-o'clock "+
			"pane for a six-o'clock start come to 6300", view.Earlier.Seconds)
	}
	if view.Totals.SummedSeconds != 4*3600 {
		t.Errorf("the day totals %ds, want the exact four hours worked however "+
			"the pane rounds its arrow", view.Totals.SummedSeconds)
	}

	// And the block is still on the pane, since half of it is visible.
	if len(view.Blocks) == 0 {
		t.Error("the visible part of a straddling block is not drawn")
	}
	for _, block := range view.Blocks {
		if block.Slot < 0 {
			t.Errorf("a block is drawn at slot %d, above the top of the pane", block.Slot)
		}
		if block.Slot+block.Span > view.Slots {
			t.Errorf("a block runs to slot %d, past the pane's %d",
				block.Slot+block.Span, view.Slots)
		}
	}

	// A block over the bottom edge is the mirror image.
	evening := newFixture(t)
	mustCreate(t, evening, at(evening.now, 16, 0), 4*3600)
	late := dayPane(t, evening, 8, 18, domain.DayOverflowArrows)
	if !late.Later.Any() {
		t.Error("a block straddling the bottom edge reported nothing below the pane")
	}
	if late.Later.Seconds <= 0 {
		t.Errorf("%ds reported below the pane, want the part that falls past six",
			late.Later.Seconds)
	}
}

// TestAPaneThatGrowsInsteadOfCounting.
//
// The other overflow rule, for somebody whose hours do not fit a fixed window.
// The pane widens until everything is on it, so nothing is counted outside -
// which is the point: with this chosen there are no arrows to read.
func TestAPaneThatGrowsInsteadOfCounting(t *testing.T) {
	f := newFixture(t)

	mustCreate(t, f, at(f.now, 5, 0), 3600)
	mustCreate(t, f, at(f.now, 22, 0), 3600)

	view := dayPane(t, f, 8, 18, domain.DayOverflowExpand)
	if view.Earlier.Any() || view.Later.Any() {
		t.Errorf("a pane set to grow still counted work outside it: %d before, "+
			"%d after", view.Earlier.Count, view.Later.Count)
	}
	if len(view.Blocks) != 2 {
		t.Errorf("%d blocks drawn, want both", len(view.Blocks))
	}
	for _, block := range view.Blocks {
		if block.Slot < 0 || block.Slot+block.Span > view.Slots {
			t.Errorf("a block at slot %d spanning %d falls outside a pane of %d "+
				"slots, which was supposed to have grown to fit it",
				block.Slot, block.Span, view.Slots)
		}
	}
}

// TestARunningTimerIsGrabbableOnThePane.
//
// A timer that has just started has no length. A block with no height cannot
// be seen or dragged, so it gets one slot - which is the difference between a
// running timer somebody can grab and one that is not there.
func TestARunningTimerIsGrabbableOnThePane(t *testing.T) {
	f := newFixture(t)
	f.now = at(f.now, 10, 0)

	if _, err := f.svc.StartTimer(f.ctx, f.assignment.ID, "just started"); err != nil {
		t.Fatalf("start: %v", err)
	}

	view := dayPane(t, f, 8, 18, domain.DayOverflowArrows)
	if len(view.Blocks) != 1 {
		t.Fatalf("%d blocks for a running timer, want 1", len(view.Blocks))
	}
	if view.Blocks[0].Span < 1 {
		t.Errorf("the running timer's block spans %d slots; a block with no "+
			"height cannot be seen or grabbed", view.Blocks[0].Span)
	}
}

// TestABudgetReportCountsItsOverruns.
//
// The figure at the top of the report: how many engagements are past their
// cap. It is what somebody looks at before reading any of the rows, so
// counting it wrong is worse than not showing it.
func TestABudgetReportCountsItsOverruns(t *testing.T) {
	f := newFixture(t)

	// Two budgeted projects: one comfortably inside, one past its cap.
	project, err := f.svc.Project(f.ctx, f.assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	project.BudgetSeconds = 100 * 3600
	if err := f.svc.UpdateProject(f.ctx, project); err != nil {
		t.Fatalf("set a budget: %v", err)
	}
	mustCreate(t, f, at(f.now, 9, 0), 4*3600)

	report, err := f.svc.BudgetReportFor(f.ctx, f.now)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if report.Overruns() != 0 {
		t.Errorf("%d overruns with four hours against a hundred", report.Overruns())
	}

	// Past the cap.
	project.BudgetSeconds = 2 * 3600
	if err := f.svc.UpdateProject(f.ctx, project); err != nil {
		t.Fatalf("shrink the budget: %v", err)
	}
	over, err := f.svc.BudgetReportFor(f.ctx, f.now)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if over.Overruns() != 1 {
		t.Errorf("%d overruns with four hours against two", over.Overruns())
	}
}

// TestTheRoundingRulesOnOffer.
//
// The presets a screen offers. Each needs a key to store and a message key to
// show, since a preset with no label is a blank option in a dropdown and one
// with no key cannot be saved.
func TestTheRoundingRulesOnOffer(t *testing.T) {
	presets := RoundingPresets()
	if len(presets) == 0 {
		t.Fatal("no rounding presets are offered, so the screen has an empty dropdown")
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		if preset.Key == "" {
			t.Error("a rounding preset has no key, so choosing it cannot be saved")
		}
		if preset.MessageKey == "" {
			t.Errorf("the preset %q has no message key, so it renders as blank", preset.Key)
		}
		if !strings.HasPrefix(preset.MessageKey, "rounding.") {
			t.Errorf("the preset %q is labelled by %q, which is not a rounding key",
				preset.Key, preset.MessageKey)
		}
		if seen[preset.Key] {
			t.Errorf("the preset %q is offered twice", preset.Key)
		}
		seen[preset.Key] = true
	}
}

// TestTheLocalModeUser.
//
// Local mode has one person and no sign-in, so the user is created on first
// run and read back on every one after. Creating a second would split one
// person's timesheet in half.
func TestTheLocalModeUser(t *testing.T) {
	f := newFixture(t)

	if err := f.svc.EnsureLocalUser(f.ctx, "Rom", "Europe/Stockholm"); err != nil {
		t.Fatalf("ensure the local user: %v", err)
	}
	if err := f.svc.EnsureLocalUser(f.ctx, "Rom", "Europe/Stockholm"); err != nil {
		t.Fatalf("ensuring twice: %v", err)
	}

	user, err := f.svc.LocalUser(f.ctx)
	if err != nil {
		t.Fatalf("read the local user: %v", err)
	}
	if user.ID == 0 {
		t.Error("no local user was created")
	}

	// The fixture already has a user, so this is really asserting that
	// ensuring does not add another.
	users, err := f.db.ListUsers(f.ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) > 2 {
		t.Errorf("%d users exist after ensuring the local one twice", len(users))
	}
}

// TestRebuildingTheSearchIndexIsRecorded.
//
// An administrative action that touches every row. It is audited like any
// other, because "search stopped finding things" is a question somebody asks
// days later and the answer is usually in the trail.
func TestRebuildingTheSearchIndexIsRecorded(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f, at(f.now, 9, 0), 3600)

	indexed, err := f.svc.ReindexSearch(f.ctx)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if indexed != 1 {
		t.Errorf("indexed %d entries, want 1", indexed)
	}

	trail, err := f.svc.AuditTrail(f.ctx, "settings", 0)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	found := false
	for _, event := range trail {
		if strings.Contains(event.Action, "reindex") {
			found = true
		}
	}
	if !found {
		t.Error("rebuilding the search index was not recorded")
	}

	// And search still works afterwards, which is the only thing a rebuild is
	// for.
	entries, _, err := f.svc.SearchEntries(f.ctx, EntryFilter{
		Query: "Development",
		From:  f.now.AddDate(0, 0, -1), To: f.now.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("%d entries found after a rebuild, want 1", len(entries))
	}

}
