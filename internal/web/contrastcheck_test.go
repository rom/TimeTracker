package web

import "testing"

func TestContrastMathIsCorrect(t *testing.T) {
	// Known values: black on white is exactly 21:1, and a colour against itself
	// is 1:1. If these are wrong, every other contrast assertion is meaningless.
	if got := contrastRatio("#000000", "#ffffff"); got < 20.9 || got > 21.1 {
		t.Errorf("black on white = %.3f:1, want 21:1", got)
	}
	if got := contrastRatio("#777777", "#777777"); got < 0.99 || got > 1.01 {
		t.Errorf("a colour against itself = %.3f:1, want 1:1", got)
	}
	// A mid grey on white is a well-known reference point: #767676 is the
	// lightest grey that passes 4.5:1 on white.
	if got := contrastRatio("#767676", "#ffffff"); got < 4.5 || got > 4.6 {
		t.Errorf("#767676 on white = %.3f:1, want just over 4.5:1", got)
	}
	// And one that must fail, so the test can actually catch a bad theme.
	if got := contrastRatio("#999999", "#ffffff"); got >= 4.5 {
		t.Errorf("#999999 on white = %.3f:1, which should not pass 4.5:1", got)
	}
}
