package web

import (
	"strings"
	"testing"
)

// The small pure helpers behind the screens.
//
// Each is a handful of lines, and each is the kind of thing that gets rewritten
// by somebody tidying up. Two of them are security boundaries.

// TestAPostLoginRedirectCannotLeaveTheSite.
//
// Without this the login form is an open redirect: an attacker sends somebody
// to /login?next=https://evil.example, they sign in legitimately, and land on a
// convincing fake that now has a plausible story for asking again.
//
// The protocol-relative case is the one that catches people out: a browser
// reads "//evil.example" as an absolute URL, and a check for a leading slash
// alone lets it through.
func TestAPostLoginRedirectCannotLeaveTheSite(t *testing.T) {
	kept := []string{
		"/today", "/week?date=2026-03-16", "/entries?q=something",
		"/terms/customer/1",
	}
	for _, next := range kept {
		t.Run("keeps "+next, func(t *testing.T) {
			if got := safeNext(next); got != next {
				t.Errorf("safeNext(%q) = %q, want it kept", next, got)
			}
		})
	}

	refused := map[string]string{
		"nothing at all":        "",
		"another site":          "https://evil.example/login",
		"protocol-relative":     "//evil.example/login",
		"a scheme with no host": "javascript:alert(1)",
		"a bare path":           "today",
		"a backslash":           `\\evil.example`,
		"a control character":   "/today\n/logout",
	}
	for name, next := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			got := safeNext(next)
			if got != "/today" {
				t.Errorf("safeNext(%q) = %q, want the default /today", next, got)
			}
		})
	}
}

// TestAnAddressIsSplitFromItsPort.
//
// Used to decide whether a request came through a trusted proxy, so getting it
// wrong either trusts everybody or nobody. IPv6 is the awkward case: the
// address is full of colons and only the last one separates the port.
func TestAnAddressIsSplitFromItsPort(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9:54321":  "203.0.113.9",
		"203.0.113.9":        "203.0.113.9",
		"[::1]:54321":        "[::1]",
		"[2001:db8::1]:8080": "[2001:db8::1]",
		"":                   "",
	}
	for addr, want := range cases {
		t.Run(addr, func(t *testing.T) {
			if got := hostOnly(addr); got != want {
				t.Errorf("hostOnly(%q) = %q, want %q", addr, got, want)
			}
		})
	}
}

// TestFileSizesReadTheWayAFileListingShowsThem.
//
// Powers of 1024 with the short suffixes, because that is what every other file
// listing on the machine shows. The boundary values are the ones worth pinning:
// 1023 bytes must not become "1.0 KB".
func TestFileSizesReadTheWayAFileListingShowsThem(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		1:             "1 B",
		1023:          "1023 B",
		1024:          "1.0 KB",
		1536:          "1.5 KB",
		1024 * 1024:   "1.0 MB",
		3 * 1 << 30:   "3.0 GB",
		2 * 1 << 40:   "2.0 TB",
		1024*1024 - 1: "1024.0 KB",
	}
	for size, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := humanBytes(size); got != want {
				t.Errorf("humanBytes(%d) = %q, want %q", size, got, want)
			}
		})
	}
}

// TestThePagerCountsRowsSomebodyCanCheck.
//
// "51-100 of 237" is a count a person can check against the rows in front of
// them. A pager that says only "page 2" leaves them to work it out, and one
// that is off by one makes them doubt the rest of the screen.
func TestThePagerCountsRowsSomebodyCanCheck(t *testing.T) {
	cases := []struct {
		name               string
		total, page        int
		pages, first, last int
		hasPages           bool
	}{
		{"nothing at all", 0, 1, 1, 0, 0, false},
		{"one short page", 10, 1, 1, 1, 10, false},
		{"exactly one full page", EntriesPerPage, 1, 1, 1, EntriesPerPage, false},
		{"one past a full page", EntriesPerPage + 1, 1, 2, 1, EntriesPerPage, true},
		{"the second page", EntriesPerPage + 1, 2, 2,
			EntriesPerPage + 1, EntriesPerPage + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := entryFilterForm{Total: tc.total, Page: tc.page}
			if got := form.Pages(); got != tc.pages {
				t.Errorf("%d rows fill %d pages, want %d", tc.total, got, tc.pages)
			}
			if got := form.HasPages(); got != tc.hasPages {
				t.Errorf("HasPages = %v, want %v", got, tc.hasPages)
			}
			if got := form.FirstShown(); got != tc.first {
				t.Errorf("first row shown = %d, want %d", got, tc.first)
			}
			if got := form.LastShown(); got != tc.last {
				t.Errorf("last row shown = %d, want %d", got, tc.last)
			}
		})
	}
}

// TestAnUnreadableFormIsTheCallersMistake.
//
// errBadForm is what tells fail() to answer 400 rather than 500. If its
// message or its identity changed, every malformed submission would start
// reading as a server fault - and would be logged as one.
func TestAnUnreadableFormIsTheCallersMistake(t *testing.T) {
	if errBadForm.Error() == "" {
		t.Error("errBadForm has no message")
	}
	if !strings.Contains(errBadForm.Error(), "form") {
		t.Errorf("errBadForm says %q, which does not mention a form", errBadForm.Error())
	}
}
