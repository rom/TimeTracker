package domain

import "sort"

// Interval is a half-open span of time in seconds since an arbitrary origin,
// used to compute how much of a day was actually covered by work.
type Interval struct {
	Start int64
	End   int64
}

// Totals is the pair of numbers this application always reports together when
// entries can overlap.
//
// Because several timers may run at once (docs/adr/0004-concurrent-timers.md),
// the sum of entry durations can exceed the wall-clock time they span. Reporting
// only the sum makes a 10-hour day over an 8-hour window look like an error or an
// over-billing; reporting only the coverage would under-state what is billable.
// So both are computed and shown side by side.
type Totals struct {
	// SummedSeconds is the sum of every entry's duration. This is what gets
	// billed.
	SummedSeconds int64
	// ElapsedSeconds is the duration of the union of the intervals: how much of
	// the day was covered by at least one entry.
	ElapsedSeconds int64
	// BillableSeconds is the summed duration of entries marked billable.
	BillableSeconds int64
	// OverlapSeconds is Summed minus Elapsed: how much time was double-counted
	// because work ran in parallel. Shown as information, never as an error.
	OverlapSeconds int64
}

// UnionSeconds returns the total length of the union of a set of intervals,
// i.e. how much time is covered by at least one of them.
//
// The intervals are sorted by start and then merged in one pass, so the cost is
// dominated by the sort. The input slice is copied first because a caller's slice
// order is not ours to change.
func UnionSeconds(intervals []Interval) int64 {
	if len(intervals) == 0 {
		return 0
	}

	sorted := make([]Interval, len(intervals))
	copy(sorted, intervals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	var total int64
	current := sorted[0]
	for _, iv := range sorted[1:] {
		if iv.Start <= current.End {
			// Overlapping or touching: extend the current run rather than
			// counting the shared time twice.
			if iv.End > current.End {
				current.End = iv.End
			}
			continue
		}
		total += current.End - current.Start
		current = iv
	}
	total += current.End - current.Start
	return total
}

// Overlaps reports whether two intervals share any time. Touching endpoints do
// not overlap, since intervals are half-open.
func (i Interval) Overlaps(other Interval) bool {
	return i.Start < other.End && other.Start < i.End
}

// UnionAccumulator computes UnionSeconds over intervals arriving one at a time.
//
// It exists so a report can be written while it is being read. UnionSeconds
// needs every interval at once, because it sorts them; a streaming export has
// nowhere to keep them, and holding a decade of intervals to compute one number
// would defeat the point of streaming at all.
//
// # Intervals must arrive in ascending order of start
//
// That is a real constraint rather than a convenience, and it is worth saying
// why descending cannot work in a single pass. Going forwards, once a run of
// overlapping intervals is closed, nothing later can reach back into it: every
// subsequent start is at or beyond the point where the run ended. Going
// backwards that is false. Given [1500,1600], then [1000,1100], then [900,2000],
// the third interval covers the first - which has already been counted and
// closed - so a single open run either double-counts it or needs to remember
// every run it has closed, at which point nothing has been saved over sorting.
//
// Out-of-order input is therefore reported rather than absorbed. A total that is
// quietly wrong is the worst outcome available here: it reaches an invoice as
// hours nobody worked, or as hours somebody did.
type UnionAccumulator struct {
	started bool
	current Interval
	total   int64
	// OutOfOrder records that an interval arrived before its predecessor. The
	// total is then unreliable, and the caller is expected to notice.
	OutOfOrder bool
}

// Add folds one interval into the union. Intervals must arrive in ascending
// order of Start; see the type comment.
func (u *UnionAccumulator) Add(interval Interval) {
	if interval.End < interval.Start {
		interval.Start, interval.End = interval.End, interval.Start
	}

	if !u.started {
		u.started = true
		u.current = interval
		return
	}

	if interval.Start < u.current.Start {
		u.OutOfOrder = true
	}

	if interval.Start <= u.current.End {
		// Overlapping or touching: extend the run rather than counting the
		// shared time twice. The same rule UnionSeconds applies after sorting.
		if interval.End > u.current.End {
			u.current.End = interval.End
		}
		return
	}

	u.total += u.current.End - u.current.Start
	u.current = interval
}

// Seconds returns the union so far.
func (u *UnionAccumulator) Seconds() int64 {
	if !u.started {
		return 0
	}
	return u.total + u.current.End - u.current.Start
}
