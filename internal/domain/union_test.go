package domain

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// TestUnionAccumulatorAgreesWithUnionSeconds.
//
// The streaming export computes its elapsed total one interval at a time,
// because it has nowhere to keep a decade of them. That is only safe if the
// incremental answer is the same as the batch one, so the two are compared over
// randomised input rather than over a handful of cases somebody thought of.
func TestUnionAccumulatorAgreesWithUnionSeconds(t *testing.T) {
	source := rand.New(rand.NewPCG(1, 2))

	for round := range 400 {
		count := 1 + source.IntN(40)
		intervals := make([]Interval, count)
		for i := range intervals {
			start := int64(source.IntN(5_000))
			// Lengths from zero upward, so touching and empty intervals - the
			// boundary cases - occur naturally rather than being special-cased.
			intervals[i] = Interval{Start: start, End: start + int64(source.IntN(600))}
		}

		want := UnionSeconds(intervals)

		sorted := slices.Clone(intervals)
		slices.SortFunc(sorted, func(a, b Interval) int { return int(a.Start - b.Start) })

		var accumulator UnionAccumulator
		for _, interval := range sorted {
			accumulator.Add(interval)
		}
		if got := accumulator.Seconds(); got != want {
			t.Fatalf("round %d: accumulated %d, UnionSeconds says %d\nintervals: %v",
				round, got, want, sorted)
		}
		if accumulator.OutOfOrder {
			t.Fatalf("round %d: sorted input reported as out of order", round)
		}
	}
}

func TestUnionAccumulatorEmpty(t *testing.T) {
	var accumulator UnionAccumulator
	if got := accumulator.Seconds(); got != 0 {
		t.Errorf("an empty accumulator totals %d, want 0", got)
	}
}

// TestUnionAccumulatorNoticesUnorderedInput.
//
// The accumulator cannot give a right answer for input it never sees in order,
// and a wrong total silently presented as a right one is the failure worth
// guarding: it would appear on an invoice as hours that were never worked, or
// hours that were.
func TestUnionAccumulatorNoticesUnorderedInput(t *testing.T) {
	for _, name := range []string{"backwards step", "fully descending"} {
		intervals := []Interval{
			{Start: 0, End: 100},
			{Start: 1000, End: 1100},
			{Start: 500, End: 600}, // back into the middle
		}
		if name == "fully descending" {
			// The case the type comment describes: a later interval covering a
			// run that has already been closed.
			intervals = []Interval{
				{Start: 1500, End: 1600},
				{Start: 1000, End: 1100},
				{Start: 900, End: 2000},
			}
		}

		var accumulator UnionAccumulator
		for _, interval := range intervals {
			accumulator.Add(interval)
		}
		if !accumulator.OutOfOrder {
			t.Errorf("%s: intervals arriving out of order should be reported, not absorbed", name)
		}
	}
}

// TestUnionAccumulatorHandlesContainedIntervals: a long entry followed by a
// short one entirely inside it must not extend the run.
func TestUnionAccumulatorHandlesContainedIntervals(t *testing.T) {
	var accumulator UnionAccumulator
	accumulator.Add(Interval{Start: 0, End: 3600})
	accumulator.Add(Interval{Start: 600, End: 1200})

	if got := accumulator.Seconds(); got != 3600 {
		t.Errorf("a contained interval changed the union: got %d, want 3600", got)
	}
}
