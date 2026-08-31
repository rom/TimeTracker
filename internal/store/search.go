package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"

	sqlite "modernc.org/sqlite"
)

// Searching entries.
//
// Three mechanisms, in decreasing order of how often they are the right answer:
//
//   * FTS5 with a trigram tokenizer, for ordinary substring search. "redir"
//     finds "login redirect", which a word-boundary tokenizer would not, and it
//     is indexed so it stays fast on a real history.
//   * LIKE, for queries too short to trigram (fewer than three characters) and
//     as the fallback if the index is unavailable.
//   * REGEXP, when the user asks for it explicitly. SQLite has no REGEXP
//     built in; it is registered below.
//
// The choice is made per query and reported back, because a search that
// silently used a different mechanism from the one asked for produces results
// nobody can explain.

// ErrInvalidQuery is returned for search text the user has to fix - a malformed
// regular expression, in practice.
//
// Its own error because the fix belongs to the person who typed it: the service
// maps it onto a validation failure so the screen says what is wrong with the
// pattern, rather than reporting an internal error nobody can act on.
var ErrInvalidQuery = errors.New("invalid search query")

// trigramMinimum is the shortest query a trigram index can look up. Below it
// there is no trigram to match, so FTS5 returns nothing at all rather than
// everything - which is why the fallback is not optional.
const trigramMinimum = 3

// SearchMode names how a query was matched.
type SearchMode string

const (
	// SearchNone means no query was given.
	SearchNone SearchMode = ""
	// SearchIndexed used the trigram full-text index.
	SearchIndexed SearchMode = "indexed"
	// SearchScan used LIKE, because the query was too short to trigram or the
	// index was unavailable.
	SearchScan SearchMode = "scan"
	// SearchRegexp used a regular expression, at the user's request.
	SearchRegexp SearchMode = "regexp"
)

// registerRegexp adds a REGEXP function to the driver.
//
// SQLite defines the REGEXP *operator* but ships no implementation, so `x
// REGEXP y` is a "no such function" error until something registers one. Go's
// regexp is RE2: linear time, no backtracking, so a pathological pattern from a
// user cannot hang the process the way a PCRE engine can. That property is why
// exposing regular expressions to users is defensible at all here.
//
// Registered once for the process, from an init function, because the driver's
// registry is global and registering twice is an error.
func init() {
	err := sqlite.RegisterDeterministicScalarFunction("regexp", 2,
		func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			pattern, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("regexp: pattern must be text")
			}
			var subject string
			switch value := args[1].(type) {
			case string:
				subject = value
			case nil:
				return int64(0), nil
			default:
				return nil, fmt.Errorf("regexp: subject must be text")
			}

			re, err := compileUserRegexp(pattern)
			if err != nil {
				return nil, err
			}
			if re.MatchString(subject) {
				return int64(1), nil
			}
			return int64(0), nil
		})
	if err != nil {
		// A registration failure means the driver changed underneath us, and
		// every regexp search would fail confusingly at query time. Failing at
		// startup is the honest response.
		panic("register the REGEXP function: " + err.Error())
	}
}

// compileUserRegexp compiles a pattern from a user, case-insensitively.
//
// Case-insensitive by default because every other search in this application
// is, and somebody typing a pattern into a search box is searching rather than
// writing a program. A pattern that needs case sensitivity can say so with the
// standard `(?-i)` flag.
func compileUserRegexp(pattern string) (*regexp.Regexp, error) {
	compiled, err := regexp.Compile(`(?i)` + pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidQuery, err)
	}
	return compiled, nil
}

// searchCondition builds the WHERE fragment for a query.
//
// It returns the mode actually used, so the caller can tell the user which of
// the three mechanisms answered them.
func searchCondition(query string, useRegexp bool) (string, []any, SearchMode, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil, SearchNone, nil
	}

	// The searchable text of an entry, assembled the same way in every branch
	// so the three mechanisms search the same words.
	const searchable = `(e.note || ' ' || a.name || ' ' || p.name || ' ' || c.name || ' ' ||
		COALESCE((SELECT group_concat(t.name, ' ') FROM entry_tags et
		            JOIN tags t ON t.id = et.tag_id WHERE et.entry_id = e.id), ''))`

	if useRegexp {
		// Validated here rather than left to fail inside SQLite, so a bad
		// pattern is a message about the pattern instead of a query error.
		if _, err := compileUserRegexp(query); err != nil {
			return "", nil, SearchNone, err
		}
		return `regexp(?, ` + searchable + `)`, []any{query}, SearchRegexp, nil
	}

	if len([]rune(query)) < trigramMinimum {
		// Too short for the index: a trigram table cannot look up a fragment
		// shorter than a trigram, and would return nothing rather than
		// everything. Scanning is correct and, for a query this short, the
		// result set is the expensive part anyway.
		return `LOWER(` + searchable + `) LIKE ?`,
			[]any{"%" + strings.ToLower(query) + "%"}, SearchScan, nil
	}

	// FTS5 with a trigram tokenizer matches substrings. The query is passed as
	// a quoted string so that a user typing "AND", "*" or a stray quote is
	// searched for literally rather than parsed as FTS5 syntax.
	return `e.id IN (SELECT rowid FROM entry_search WHERE entry_search MATCH ?)`,
		[]any{fts5Quote(query)}, SearchIndexed, nil
}

// fts5Quote wraps a query as an FTS5 string literal.
//
// Everything a user types is searched for literally. FTS5's own query language
// has operators (AND, OR, NOT, NEAR, *, ^) and a user typing "R&D" or "C++"
// means those characters, not a syntax error. A doubled quote is the escape.
func fts5Quote(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}

// IndexEntryTx writes an entry's searchable text into the index.
//
// Called from the same transaction as the entry write. The index is not an
// external-content table because the text spans four joined tables and a tag
// list; no trigger could keep that right, so the application maintains it
// explicitly and the sweep in ReindexSearch repairs it if it ever drifts.
func IndexEntryTx(ctx context.Context, tx *sql.Tx, entryID int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entry_search WHERE rowid = ?`, entryID); err != nil {
		return fmt.Errorf("clear search index: %w", err)
	}

	var note, assignment, project, customer string
	var tags sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT e.note, a.name, p.name, c.name,
		       (SELECT group_concat(t.name, ' ') FROM entry_tags et
		          JOIN tags t ON t.id = et.tag_id WHERE et.entry_id = e.id)
		FROM time_entries e
		JOIN assignments a ON a.id = e.assignment_id
		JOIN projects    p ON p.id = a.project_id
		JOIN customers   c ON c.id = p.customer_id
		WHERE e.id = ?`, entryID).Scan(&note, &assignment, &project, &customer, &tags)
	if err != nil {
		return fmt.Errorf("read entry for indexing: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO entry_search (rowid, note, assignment, project, customer, tags)
		VALUES (?, ?, ?, ?, ?, ?)`,
		entryID, note, assignment, project, customer, tags.String)
	if err != nil {
		return fmt.Errorf("write search index: %w", err)
	}
	return nil
}

// ReindexSearch rebuilds the whole index.
//
// Needed after a restore, after a rename that touches many entries, and as the
// repair for an index that has drifted. It is not run at startup: on a large
// history it is expensive, and an index that is merely stale degrades search
// rather than breaking the application.
func (db *DB) ReindexSearch(ctx context.Context) (int, error) {
	return db.reindexSearch(ctx)
}

func (db *DB) reindexSearch(ctx context.Context) (int, error) {
	count := 0
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		count, txErr = ReindexSearchTx(ctx, tx)
		return txErr
	})
	return count, err
}

// ReindexSearchTx rebuilds the index on the caller's executor and reports how
// many entries it wrote.
//
// The transactional form is what lets a tag rename rebuild the index and record
// itself in one go. It also has to run *inside* the caller's transaction rather
// than beside it: the rename is not visible to another connection until the
// commit, so a rebuild on its own would index the old name and quietly undo the
// change as far as search is concerned.
func ReindexSearchTx(ctx context.Context, db Execer) (int, error) {
	if _, err := db.ExecContext(ctx, `DELETE FROM entry_search`); err != nil {
		return 0, fmt.Errorf("clear search index: %w", err)
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO entry_search (rowid, note, assignment, project, customer, tags)
		SELECT e.id, e.note, a.name, p.name, c.name,
		       COALESCE((SELECT group_concat(t.name, ' ') FROM entry_tags et
		                   JOIN tags t ON t.id = et.tag_id WHERE et.entry_id = e.id), '')
		FROM time_entries e
		JOIN assignments a ON a.id = e.assignment_id
		JOIN projects    p ON p.id = a.project_id
		JOIN customers   c ON c.id = p.customer_id`)
	if err != nil {
		return 0, fmt.Errorf("rebuild search index: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
