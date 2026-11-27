package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The form must not decide whether time is chargeable.
//
// The add form on Today carried <input type="checkbox" name="billable"
// checked> for years. An assignment's billable_default was therefore ignored
// by the one path most time is recorded through, and internal time arrived in
// a client's Amount column. The decision belongs to the service, which has the
// assignment in hand (see billableFor); the form's only job is to say whether
// anybody expressed an opinion.
//
// This test is markup-shaped because the defect was: nothing about the Go
// changed, and nothing about the Go would have caught it.
func TestNoFormDecidesBillingOnItsOwn(t *testing.T) {
	// Forms that create time against an assignment. Each must defer, because
	// each is drawn before an assignment has been chosen.
	for _, name := range []string{"page_today.html", "page_routines.html"} {
		source, err := os.ReadFile(filepath.Join("templates", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		markup := string(source)

		if !strings.Contains(markup, `name="billable_source" value="assignment"`) {
			t.Errorf(`%s has no billable_source marker, so the service cannot `+
				`tell a deliberate "no" from a box nobody looked at, and the `+
				`assignment's own default is ignored`, name)
		}

		// A ticked box with no condition around it is the defect itself.
		box := regexp.MustCompile(`<input[^>]*name="billable"[^>]*>`)
		for _, tag := range box.FindAllString(markup, -1) {
			if !strings.Contains(tag, "checked") {
				continue
			}
			// Conditional, i.e. showing what an existing record says, is fine.
			if strings.Contains(tag, "{{if") {
				continue
			}
			t.Errorf("%s: %s is ticked unconditionally. Whether time is "+
				"chargeable is the assignment's answer until somebody says "+
				"otherwise, and this guesses before an assignment is chosen",
				name, strings.TrimSpace(tag))
		}
	}

	// The correction form is the deliberate exception, and it has to stay one:
	// editing an entry states a value, and sending the marker there would
	// revert every saved correction to the assignment's default.
	source, err := os.ReadFile(filepath.Join("templates", "fragments.html"))
	if err != nil {
		t.Fatalf("read fragments.html: %v", err)
	}
	if strings.Contains(string(source), `name="billable_source"`) {
		t.Error("the correction form must not send billable_source: editing " +
			"an entry is a statement about that entry, not a request to " +
			"follow the assignment again")
	}
}
