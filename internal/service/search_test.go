package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Tags and search.
//
// The three search mechanisms are tested for the property that distinguishes
// them, not just for returning rows: trigram finds a fragment inside a word,
// which is the reason for choosing it; a short query has to fall back rather
// than return nothing; and a regular expression has to be a regular expression
// rather than a literal.

// tagged records an entry with a note and tags.
func (f *fixture) tagged(t *testing.T, note string, tags ...string) domain.TimeEntry {
	t.Helper()
	entry, err := f.svc.CreateEntry(f.ctx, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: f.now,
		DurationSeconds: 3600, Billable: true, Note: note, Tags: tags,
	})
	if err != nil {
		t.Fatalf("create %q: %v", note, err)
	}
	f.advance(2 * time.Hour)
	return entry
}

// search runs a query over everything.
func (f *fixture) search(t *testing.T, filter EntryFilter) ([]domain.TimeEntry, store.SearchMode) {
	t.Helper()
	filter.From = f.now.AddDate(-1, 0, 0)
	filter.To = f.now.AddDate(1, 0, 0)
	entries, mode, err := f.svc.SearchEntries(f.ctx, filter)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return entries, mode
}

// TestTagsSurviveTheRoundTrip. They were parsed by quick-add from the first
// release and thrown away, which made "#travel" a way of deleting a word from
// your own note.
func TestTagsSurviveTheRoundTrip(t *testing.T) {
	f := newFixture(t)
	created := f.tagged(t, "incident call", "Incident", "  urgent  ", "incident")

	stored, err := f.svc.Entry(f.ctx, created.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	// Normalised and de-duplicated: "#Incident" and "incident" are one tag,
	// because a filter that misses two thirds of what somebody tagged is worse
	// than no filter.
	if len(stored.Tags) != 2 {
		t.Fatalf("tags = %v, want two after normalising", stored.Tags)
	}
	for _, tag := range stored.Tags {
		if tag != strings.ToLower(strings.TrimSpace(tag)) {
			t.Errorf("tag %q was stored unnormalised", tag)
		}
	}

	// An edit replaces the set rather than adding to it: the form shows every
	// tag, so what comes back is the complete answer.
	updated, err := f.svc.UpdateEntry(f.ctx, created.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: created.StartedAt,
		DurationSeconds: 3600, Billable: true, Note: "incident call", Tags: []string{"resolved"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "resolved" {
		t.Errorf("tags after edit = %v, want just [resolved]", updated.Tags)
	}
}

// TestQuickAddTagsReachTheEntry, through the parser that has always found them.
func TestQuickAddTagsReachTheEntry(t *testing.T) {
	f := newFixture(t)

	entry, parsed, err := f.svc.QuickAdd(f.ctx, "2h Development fixed the login redirect #incident #urgent")
	if err != nil {
		t.Fatalf("quick add: %v", err)
	}
	if parsed.Ambiguous {
		t.Fatalf("the line was not understood: %s", parsed.Reason)
	}
	stored, err := f.svc.Entry(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(stored.Tags) != 2 {
		t.Errorf("tags = %v, want the two from the line", stored.Tags)
	}
	// And the tags are not left in the note.
	if strings.Contains(stored.Note, "#incident") {
		t.Errorf("the tag stayed in the note: %q", stored.Note)
	}
}

// TestTagFilterWantsAllOfThem, not any: asking for two tags means entries
// carrying both, which is what somebody looking for a specific slice expects.
func TestTagFilterWantsAllOfThem(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "both", "incident", "urgent")
	f.tagged(t, "one", "incident")
	f.tagged(t, "other", "urgent")

	if entries, _ := f.search(t, EntryFilter{Tags: []string{"incident"}}); len(entries) != 2 {
		t.Errorf("one tag matched %d entries, want 2", len(entries))
	}
	entries, _ := f.search(t, EntryFilter{Tags: []string{"incident", "urgent"}})
	if len(entries) != 1 {
		t.Fatalf("two tags matched %d entries, want only the one carrying both", len(entries))
	}
	if entries[0].Note != "both" {
		t.Errorf("matched %q, want the entry with both tags", entries[0].Note)
	}
}

// TestSearchFindsAFragmentInsideAWord is the reason for the trigram tokenizer.
// A word-boundary tokenizer cannot answer this, and it is the search people
// actually type.
func TestSearchFindsAFragmentInsideAWord(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "fixed the login redirect")
	f.tagged(t, "database migration")

	entries, mode := f.search(t, EntryFilter{Query: "edirec"})
	if mode != store.SearchIndexed {
		t.Errorf("mode = %q, want the index", mode)
	}
	if len(entries) != 1 || entries[0].Note != "fixed the login redirect" {
		t.Fatalf("searching for a fragment inside a word matched %d entries", len(entries))
	}
}

// TestSearchCoversEverythingAnEntryIsFindableBy: not just the note.
func TestSearchCoversTheWholeEntry(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "no useful words here", "onboarding")

	for _, query := range []string{
		"onboarding", // the tag
		"Developm",   // the assignment
		"Migrati",    // the project
		"Acme",       // the customer
	} {
		if entries, _ := f.search(t, EntryFilter{Query: query}); len(entries) != 1 {
			t.Errorf("searching %q matched %d entries, want 1", query, len(entries))
		}
	}
}

// TestShortSearchFallsBackRatherThanReturningNothing. A trigram index cannot
// look up a fragment shorter than a trigram, so without the fallback a
// two-character search would silently return an empty page.
func TestShortSearchFallsBackRatherThanReturningNothing(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "database migration")

	entries, mode := f.search(t, EntryFilter{Query: "da"})
	if mode != store.SearchScan {
		t.Errorf("mode = %q, want a scan for a two-character query", mode)
	}
	if len(entries) != 1 {
		t.Errorf("a short query matched %d entries, want 1", len(entries))
	}
}

// TestSearchTreatsTheQueryLiterally. FTS5 has its own query language, and a
// user typing "C++" or a stray quote means those characters rather than syntax.
func TestSearchTreatsTheQueryLiterally(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, `upgraded to C++ 20`)
	f.tagged(t, `wrote "the" report`)

	for _, query := range []string{"C++", `"the"`, "d to C"} {
		entries, _, err := f.svc.SearchEntries(f.ctx, EntryFilter{
			From: f.now.AddDate(-1, 0, 0), To: f.now.AddDate(1, 0, 0), Query: query,
		})
		if err != nil {
			t.Fatalf("searching %q errored: %v", query, err)
		}
		if len(entries) == 0 {
			t.Errorf("searching %q matched nothing", query)
		}
	}
}

// TestRegexpSearchIsARegularExpression, and a broken one is the searcher's
// mistake rather than an internal error.
func TestRegexpSearch(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "rollback plan")
	f.tagged(t, "rollforward plan")
	f.tagged(t, "nothing relevant")

	entries, mode := f.search(t, EntryFilter{Query: "roll(back|forward)", UseRegexp: true})
	if mode != store.SearchRegexp {
		t.Errorf("mode = %q, want regexp", mode)
	}
	if len(entries) != 2 {
		t.Errorf("the alternation matched %d entries, want 2", len(entries))
	}

	// Case-insensitive by default: somebody typing into a search box is
	// searching, not writing a program.
	if entries, _ := f.search(t, EntryFilter{Query: "ROLLBACK", UseRegexp: true}); len(entries) != 1 {
		t.Errorf("an upper-case pattern matched %d entries, want 1", len(entries))
	}

	_, _, err := f.svc.SearchEntries(f.ctx, EntryFilter{Query: "[unclosed", UseRegexp: true})
	if err == nil {
		t.Fatal("a malformed pattern was accepted")
	}
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("error = %v, want one the searcher can act on", err)
	}
}

// TestSearchIndexFollowsEdits: an entry whose note changed has to be findable by
// the new words and not by the old ones.
func TestSearchIndexFollowsEdits(t *testing.T) {
	f := newFixture(t)
	entry := f.tagged(t, "original wording", "before")

	if _, err := f.svc.UpdateEntry(f.ctx, entry.ID, EntryInput{
		AssignmentID: f.assignment.ID, StartedAt: entry.StartedAt,
		DurationSeconds: 3600, Billable: true,
		Note: "replacement wording", Tags: []string{"after"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if entries, _ := f.search(t, EntryFilter{Query: "replacement"}); len(entries) != 1 {
		t.Errorf("the new wording matched %d entries, want 1", len(entries))
	}
	if entries, _ := f.search(t, EntryFilter{Query: "original"}); len(entries) != 0 {
		t.Errorf("the old wording still matches %d entries", len(entries))
	}
	if entries, _ := f.search(t, EntryFilter{Query: "after"}); len(entries) != 1 {
		t.Errorf("the new tag matched %d entries, want 1", len(entries))
	}
}

// TestRenamingATagKeepsEntriesFindable: the index carries tag names, so a
// rename has to reach it.
func TestRenamingATagKeepsEntriesFindable(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "some work", "incdient") // as typed, with the typo

	tags, err := f.svc.Tags(f.ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("tags = %+v, want one", tags)
	}
	if tags[0].EntryCount != 1 {
		t.Errorf("entry count = %d, want 1", tags[0].EntryCount)
	}

	tags[0].Name = "incident"
	if err := f.svc.UpdateTag(f.ctx, tags[0]); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if entries, _ := f.search(t, EntryFilter{Query: "incident"}); len(entries) != 1 {
		t.Errorf("the renamed tag matched %d entries, want 1", len(entries))
	}
	if entries, _ := f.search(t, EntryFilter{Tags: []string{"incident"}}); len(entries) != 1 {
		t.Errorf("filtering by the renamed tag matched %d entries, want 1", len(entries))
	}
}

// TestDeletingATagLeavesTheEntry. Nothing is invoiced against a tag, so removing
// one loses a label rather than a record.
func TestDeletingATagLeavesTheEntry(t *testing.T) {
	f := newFixture(t)
	entry := f.tagged(t, "some work", "temporary")

	tags, err := f.svc.Tags(f.ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if err := f.svc.DeleteTag(f.ctx, tags[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	stored, err := f.svc.Entry(f.ctx, entry.ID)
	if err != nil {
		t.Fatalf("the entry went with the tag: %v", err)
	}
	if len(stored.Tags) != 0 {
		t.Errorf("the entry still carries %v", stored.Tags)
	}
	if stored.DurationSeconds != 3600 {
		t.Error("the entry's own data changed when its tag was deleted")
	}
}

// TestRenamingOntoAnExistingTagIsRefused. Merging two tags is a different,
// destructive operation and should not happen because of a typo in a rename box.
func TestRenamingOntoAnExistingTagIsRefused(t *testing.T) {
	f := newFixture(t)
	f.tagged(t, "one", "incident")
	f.tagged(t, "two", "urgent")

	tags, err := f.svc.Tags(f.ctx)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	tags[0].Name = tags[1].Name
	if err := f.svc.UpdateTag(f.ctx, tags[0]); err == nil {
		t.Fatal("renaming onto an existing tag was accepted")
	} else if !errors.Is(err, ErrConflict) {
		t.Errorf("error = %v, want a conflict", err)
	}
}
