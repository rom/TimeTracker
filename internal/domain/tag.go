package domain

import (
	"strings"
	"time"
)

// A tag is a cross-cutting label on a time entry.
//
// It answers a different question from the customer/project/assignment
// hierarchy. That hierarchy answers "who is invoiced for this", and every entry
// has exactly one path through it. A tag answers "what sort of work was this" -
// #incident, #onboarding, #travel, #billable-review - which cuts across the
// hierarchy, belongs to no level of it, and can apply several at once.
//
// Keeping them separate is what stops the hierarchy from being abused as a
// labelling system, which is how project lists end up with forty entries named
// after activities rather than after work somebody agreed to pay for.

// Tag is a label.
type Tag struct {
	ID int64
	// Name is stored lower-cased and trimmed; see NormaliseTag.
	Name      string
	ColourKey string
	CreatedAt time.Time

	// EntryCount is filled by the listing query, so the management screen can
	// show which tags are actually used.
	EntryCount int
}

// Validate checks the rules that hold regardless of storage.
func (t Tag) Validate() error {
	name := NormaliseTag(t.Name)
	if name == "" {
		return invalid("a tag needs a name")
	}
	if len(name) > 60 {
		return invalid("tag name is too long (max 60 characters)")
	}
	return nil
}

// NormaliseTag puts a tag into the one form it is stored and compared in.
//
// Lower-cased, trimmed, inner whitespace collapsed to a hyphen, and a leading
// "#" removed so that typing the sigil into a tag field does not create a tag
// literally called "#travel".
//
// One function, used by the parser, the form and the importer alike: "#Travel",
// "travel" and " Travel " have to be one tag, because a filter that misses two
// thirds of the entries somebody tagged is worse than no filter.
func NormaliseTag(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.TrimPrefix(name, "#")
	name = strings.TrimSpace(name)

	// Collapse any run of whitespace into a single hyphen, so "code review"
	// and "code  review" are one tag and neither is two.
	fields := strings.Fields(name)
	return strings.Join(fields, "-")
}

// NormaliseTags cleans a list, dropping empties and duplicates while keeping
// the order they were given in.
func NormaliseTags(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	var out []string
	for _, candidate := range raw {
		name := NormaliseTag(candidate)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ParseTagList reads tags as typed into a form field: comma or space separated,
// with or without the # sigil.
func ParseTagList(raw string) []string {
	replaced := strings.ReplaceAll(raw, ",", " ")
	return NormaliseTags(strings.Fields(replaced))
}

// FormatTagList renders tags back into the form a field accepts, so what is
// displayed can be typed back.
func FormatTagList(tags []string) string { return strings.Join(tags, ", ") }
