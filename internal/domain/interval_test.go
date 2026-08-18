package domain

import "testing"

// TestUnionSeconds is the arithmetic behind the "elapsed" total shown alongside
// the summed total wherever entries can overlap (ASR-001).
func TestUnionSeconds(t *testing.T) {
	tests := []struct {
		name string
		in   []Interval
		want int64
	}{
		{"empty", nil, 0},
		{"single", []Interval{{0, 3600}}, 3600},
		{"disjoint", []Interval{{0, 3600}, {7200, 10800}}, 7200},
		{"touching merges", []Interval{{0, 3600}, {3600, 7200}}, 7200},
		{"partial overlap counted once", []Interval{{0, 3600}, {1800, 5400}}, 5400},
		{"fully contained", []Interval{{0, 7200}, {1800, 3600}}, 7200},
		{"unsorted input", []Interval{{7200, 10800}, {0, 3600}}, 7200},
		{"three overlapping", []Interval{{0, 3600}, {1800, 5400}, {3600, 9000}}, 9000},
		{"identical", []Interval{{0, 3600}, {0, 3600}}, 3600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnionSeconds(tc.in); got != tc.want {
				t.Errorf("UnionSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestUnionSecondsDoesNotMutate: callers pass slices they still own.
func TestUnionSecondsDoesNotMutate(t *testing.T) {
	in := []Interval{{7200, 10800}, {0, 3600}}
	UnionSeconds(in)
	if in[0].Start != 7200 {
		t.Errorf("input slice was reordered: %+v", in)
	}
}

func TestOverlaps(t *testing.T) {
	a := Interval{0, 3600}
	if !a.Overlaps(Interval{1800, 5400}) {
		t.Error("partial overlap not detected")
	}
	if a.Overlaps(Interval{3600, 7200}) {
		t.Error("touching intervals must not count as overlapping (half-open)")
	}
	if a.Overlaps(Interval{7200, 10800}) {
		t.Error("disjoint intervals reported as overlapping")
	}
}
