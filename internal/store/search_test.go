package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Search: three mechanisms, one box.
//
// A person types words into a field and expects to find their own notes. What
// happens underneath depends on the length of what they typed and on whether
// they asked for a regular expression - a scan, an FTS5 trigram index, or a
// compiled pattern - and the thing that must be true of all three is that the
// text is searched for *literally*.
//
// That is not automatic. FTS5 has its own query language, and a note containing
// "R&D", "C++" or a stray quote is a syntax error rather than a search unless
// the query is quoted. A regexp from a user is a program, and an invalid one has
// to come back as a message about the pattern rather than as a database error on
// a screen.

// searchFixture seeds entries whose notes are the awkward cases.
func searchFixture(t *testing.T) (*DB, domain.User) {
	t.Helper()

	db := newTestDB(t)
	ctx := context.Background()
	user, assignment := seed(t, db)

	start := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	for i, note := range []string{
		"Rewrote the invoice importer",
		`Quoted "the specification" back at them`,
		"R&D budget meeting",
		"Upgraded the C++ toolchain",
		"Fika",
		"Åtgärdade felet i årsrapporten",
	} {
		at := start.Add(time.Duration(i) * time.Hour)
		end := at.Add(30 * time.Minute)
		entry := domain.TimeEntry{
			UserID: user.ID, EnteredBy: user.ID, AssignmentID: assignment.ID,
			StartedAt: at, EndedAt: &end, DurationSeconds: 1800, Note: note,
			Status: domain.StatusConfirmed, TimeZone: "UTC",
		}
		// Written the way the application writes an entry: the row, its tags and
		// its index entry in one transaction. db.CreateEntry alone does not
		// index, and a fixture built with it would leave every indexed search
		// below finding nothing - which would look like a bug in the search.
		if err := db.InTx(ctx, func(tx *sql.Tx) error {
			_, err := CreateEntryWithTagsTx(ctx, tx, entry)
			return err
		}); err != nil {
			t.Fatalf("create entry %q: %v", note, err)
		}
	}
	return db, user
}

// find runs a search and returns the notes that matched.
func find(t *testing.T, db *DB, user domain.User, query string, useRegexp bool) []string {
	t.Helper()

	entries, _, err := db.SearchEntries(context.Background(), EntryFilter{
		UserID: user.ID, Query: query, UseRegexp: useRegexp,
		Scope: UnrestrictedScope(),
	})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	var notes []string
	for _, entry := range entries {
		notes = append(notes, entry.Note)
	}
	return notes
}

// TestSearchingForCharactersThatMeanSomethingToFTS5.
//
// Every one of these is a real note somebody writes, and every one of them is
// FTS5 syntax if it reaches the index unquoted. The failure without the quoting
// is not "no results" - it is an error from the database on the search screen.
func TestSearchingForCharactersThatMeanSomethingToFTS5(t *testing.T) {
	db, user := searchFixture(t)

	for _, search := range []struct {
		query string
		want  string
	}{
		{"R&D budget", "R&D budget meeting"},
		{`"the specification"`, `Quoted "the specification" back at them`},
		{"C++ toolchain", "Upgraded the C++ toolchain"},
		{"invoice importer", "Rewrote the invoice importer"},
	} {
		notes := find(t, db, user, search.query, false)
		var found bool
		for _, note := range notes {
			if note == search.want {
				found = true
			}
		}
		if !found {
			t.Errorf("searching for %q did not find %q; got %v",
				search.query, search.want, notes)
		}
	}

	// The other half of "literally": FTS5's operators are not operators here.
	// "budget AND meeting" is a substring nothing contains, so it finds nothing
	// - rather than erroring, and rather than quietly becoming a boolean query
	// that would make the same words behave differently depending on whether
	// somebody happened to capitalise them.
	if notes := find(t, db, user, "budget AND meeting", false); len(notes) != 0 {
		t.Errorf("FTS5 operators are being interpreted: %v", notes)
	}
}

// TestAShortQueryStillSearches.
//
// Shorter than a trigram, so the index cannot answer it: the query falls back to
// a scan. Without that fallback a two-letter search returns nothing at all,
// which reads as "you have no entries about this" rather than "ask differently".
func TestAShortQueryStillSearches(t *testing.T) {
	db, user := searchFixture(t)

	notes := find(t, db, user, "R&", false)
	if len(notes) == 0 {
		t.Error("a query shorter than a trigram found nothing; the scan fallback " +
			"is what makes a short search mean something")
	}
}

// TestSearchIsCaseInsensitiveAndFindsNonASCII.
//
// Both halves matter in Swedish, which is one of the two catalogued languages: a
// search for "årsrapport" that misses "Årsrapporten" is a search that does not
// work for half the interface.
func TestSearchIsCaseInsensitiveAndFindsNonASCII(t *testing.T) {
	db, user := searchFixture(t)

	for _, query := range []string{"åtgärdade", "ÅTGÄRDADE", "årsrapporten"} {
		if notes := find(t, db, user, query, false); len(notes) == 0 {
			t.Errorf("searching for %q found nothing", query)
		}
	}
}

// TestARegexpSearchIsAPattern.
//
// The point of offering regular expressions at all: a question a substring
// cannot ask. Case-insensitive by default, because somebody typing into a search
// box is searching rather than writing a program - and the standard flag is
// there for anybody who means otherwise.
func TestARegexpSearchIsAPattern(t *testing.T) {
	db, user := searchFixture(t)

	notes := find(t, db, user, "invoice|toolchain", true)
	if len(notes) != 2 {
		t.Errorf("the alternation matched %d entries, want 2: %v", len(notes), notes)
	}

	if notes := find(t, db, user, "REWROTE", true); len(notes) != 1 {
		t.Errorf("a regexp search is case-sensitive by default: %v", notes)
	}
	if notes := find(t, db, user, "(?-i)REWROTE", true); len(notes) != 0 {
		t.Errorf("(?-i) did not make the pattern case-sensitive: %v", notes)
	}
	// Anchors apply to the whole searchable text, which is the note followed by
	// the assignment, project and customer names - so ^ matches the start of the
	// note and $ is somewhere past the customer. Worth pinning, because a reader
	// would reasonably expect the pattern to be matched against the note alone.
	if notes := find(t, db, user, "^Fika", true); len(notes) != 1 {
		t.Errorf("^ does not anchor at the start of the note: %v", notes)
	}
	if notes := find(t, db, user, "^toolchain", true); len(notes) != 0 {
		t.Errorf("^ matched in the middle of the text: %v", notes)
	}
}

// TestAnInvalidRegexpIsAMessageAboutThePattern.
//
// Validated before it reaches SQLite, so a mistyped pattern is something the
// person can fix rather than a database error the interface has to apologise
// for. It is a distinct error type because the layer above renders it as a 400
// against the search box.
func TestAnInvalidRegexpIsAMessageAboutThePattern(t *testing.T) {
	db, user := searchFixture(t)

	_, _, err := db.SearchEntries(context.Background(), EntryFilter{
		UserID: user.ID, Query: "unclosed(", UseRegexp: true,
		Scope: UnrestrictedScope(),
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("a malformed pattern: got %v, want ErrInvalidQuery", err)
	}
	if err != nil && !strings.Contains(err.Error(), "unclosed") {
		t.Errorf("the error does not say what was wrong with the pattern: %v", err)
	}
}

// TestSearchReportsWhichMechanismAnswered.
//
// The screen tells the person how their search was run, which is what makes the
// difference between "no results" and "your query was too short to index"
// visible. It is also the only way a test can tell that the index is being used
// at all rather than everything quietly falling back to a scan.
func TestSearchReportsWhichMechanismAnswered(t *testing.T) {
	db, user := searchFixture(t)
	ctx := context.Background()

	for _, search := range []struct {
		query  string
		regexp bool
		want   SearchMode
	}{
		{"", false, SearchNone},
		{"ab", false, SearchScan},
		{"invoice", false, SearchIndexed},
		{"invoice|budget", true, SearchRegexp},
	} {
		_, mode, err := db.SearchEntries(ctx, EntryFilter{
			UserID: user.ID, Query: search.query, UseRegexp: search.regexp,
			Scope: UnrestrictedScope(),
		})
		if err != nil {
			t.Fatalf("search %q: %v", search.query, err)
		}
		if mode != search.want {
			t.Errorf("searching %q used %v, want %v", search.query, mode, search.want)
		}
	}
}

// TestQuotingAQuery.
//
// The escape itself, at the unit. A doubled quote is FTS5's escape, and the
// property that matters is that whatever a person types comes out as one string
// literal rather than as several tokens with operators between them.
func TestQuotingAQuery(t *testing.T) {
	for query, want := range map[string]string{
		`plain`:       `"plain"`,
		`two words`:   `"two words"`,
		`say "hello"`: `"say ""hello"""`,
		`"`:           `""""`,
		`AND OR NOT`:  `"AND OR NOT"`,
		`prefix*`:     `"prefix*"`,
		`^caret`:      `"^caret"`,
		`R&D`:         `"R&D"`,
	} {
		if got := fts5Quote(query); got != want {
			t.Errorf("fts5Quote(%q) = %q, want %q", query, got, want)
		}
	}
}

// TestCompilingAUserPattern.
//
// Case-insensitivity is applied by prefixing the flag, which means a pattern
// that sets its own flags has to still work - `(?-i)` and `(?s)` are both things
// somebody will paste in from elsewhere.
func TestCompilingAUserPattern(t *testing.T) {
	for _, pattern := range []string{"plain", "(?-i)Exact", "(?s)any.thing", "^anchored$"} {
		if _, err := compileUserRegexp(pattern); err != nil {
			t.Errorf("compiling %q failed: %v", pattern, err)
		}
	}
	for _, pattern := range []string{"unclosed(", "[a-", "*invalid", `\`} {
		if _, err := compileUserRegexp(pattern); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("compiling %q: got %v, want ErrInvalidQuery", pattern, err)
		}
	}

	// The prefix is a flag rather than a wrapper, so an alternation still spans
	// the whole pattern: (?i)a|b must mean "a or b", not "(?i)a" or "b".
	compiled, err := compileUserRegexp("alpha|beta")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !compiled.MatchString("BETA") {
		t.Error("case-insensitivity does not reach the second branch of an alternation")
	}
}

// TestReindexingRepairsTheSearchIndex.
//
// The index is maintained by the application rather than by a trigger, because
// the searchable text spans four joined tables and a tag list. That means it can
// drift, and the sweep is the repair - so it has to actually rebuild rather than
// merely run.
func TestReindexingRepairsTheSearchIndex(t *testing.T) {
	db, user := searchFixture(t)
	ctx := context.Background()

	// Corrupt the index the way drift would: empty it.
	if err := db.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM entry_search`)
		return err
	}); err != nil {
		t.Fatalf("empty the index: %v", err)
	}
	if notes := find(t, db, user, "invoice importer", false); len(notes) != 0 {
		t.Fatalf("the index still answers after being emptied: %v", notes)
	}

	indexed, err := db.ReindexSearch(ctx)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if indexed != 6 {
		t.Errorf("reindexed %d entries, want the 6 in the fixture", indexed)
	}
	if notes := find(t, db, user, "invoice importer", false); len(notes) != 1 {
		t.Errorf("reindexing did not restore the entry: %v", notes)
	}
}
