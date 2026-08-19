package domain

import "testing"

// Tag normalisation.
//
// One function decides what "#Travel", "travel" and " Travel " all are, and
// every path into the application uses it: the quick-add parser, the entry
// form, the importer, a restore. If they disagreed, a filter would miss two
// thirds of what somebody had tagged - which is worse than no filter, because
// it looks like an answer.

func TestNormaliseTag(t *testing.T) {
	cases := map[string]string{
		"travel":        "travel",
		"Travel":        "travel",
		"  Travel  ":    "travel",
		"#travel":       "travel",
		"#Travel":       "travel",
		"# travel":      "travel",
		"code review":   "code-review",
		"code   review": "code-review",
		"":              "",
		"   ":           "",
		"#":             "",
	}
	for input, want := range cases {
		if got := NormaliseTag(input); got != want {
			t.Errorf("NormaliseTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormaliseTagsDeduplicates(t *testing.T) {
	got := NormaliseTags([]string{"Travel", "#travel", "urgent", "", "  ", "URGENT"})
	if len(got) != 2 {
		t.Fatalf("got %v, want two distinct tags", got)
	}
	// Order is the order they were given in: a form that reshuffled somebody's
	// tags on save would look like it had lost one.
	if got[0] != "travel" || got[1] != "urgent" {
		t.Errorf("got %v, want [travel urgent]", got)
	}
}

func TestParseAndFormatTagList(t *testing.T) {
	cases := map[string][]string{
		"incident, urgent":  {"incident", "urgent"},
		"incident urgent":   {"incident", "urgent"},
		"#incident,#urgent": {"incident", "urgent"},
		"  incident  ":      {"incident"},
		"":                  nil,
		",,,":               nil,
	}
	for input, want := range cases {
		got := ParseTagList(input)
		if len(got) != len(want) {
			t.Errorf("ParseTagList(%q) = %v, want %v", input, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("ParseTagList(%q) = %v, want %v", input, got, want)
				break
			}
		}
	}

	// What is displayed can be typed back, which is what makes the field
	// editable rather than merely readable.
	round := ParseTagList(FormatTagList([]string{"incident", "code-review"}))
	if len(round) != 2 || round[0] != "incident" || round[1] != "code-review" {
		t.Errorf("round trip gave %v", round)
	}
}

func TestTagValidate(t *testing.T) {
	if err := (Tag{Name: "incident"}).Validate(); err != nil {
		t.Errorf("a valid tag was rejected: %v", err)
	}
	for _, bad := range []string{"", "   ", "#"} {
		if err := (Tag{Name: bad}).Validate(); err == nil {
			t.Errorf("accepted %q as a tag name", bad)
		}
	}
	long := ""
	for i := 0; i < 70; i++ {
		long += "x"
	}
	if err := (Tag{Name: long}).Validate(); err == nil {
		t.Error("accepted an over-long tag name")
	}
}
