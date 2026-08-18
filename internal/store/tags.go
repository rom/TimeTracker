package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Tag storage.
//
// Tags are looked up by name rather than by id from the caller's side: a person
// types "#incident", not a number, and every path into the application - the
// quick-add parser, the entry form, the CSV importer, a restore - has a name in
// hand. Resolving a name to a row, creating it if it is new, therefore happens
// here rather than being repeated at each of those call sites.

// EnsureTagsTx resolves tag names to ids, creating any that do not exist.
//
// Inside the caller's transaction, because tagging an entry and creating the
// tags it needs have to succeed or fail together: a tag row left behind by a
// failed save would appear in the filter list attached to nothing.
func EnsureTagsTx(ctx context.Context, tx *sql.Tx, names []string) ([]int64, error) {
	names = domain.NormaliseTags(names)
	if len(names) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(names))
	for _, name := range names {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ?`, name).Scan(&id)
		switch {
		case err == nil:
			ids = append(ids, id)
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("look up tag %q: %w", name, err)
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO tags (name, colour_key, created_at) VALUES (?, 'slate', ?)`,
			name, formatTime(time.Now()))
		if err != nil {
			return nil, fmt.Errorf("create tag %q: %w", name, err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// SetEntryTagsTx replaces an entry's tags.
//
// Replace rather than merge: the form shows every tag the entry has, so what
// comes back is the complete set, and a tag the user removed has to actually go.
func SetEntryTagsTx(ctx context.Context, tx *sql.Tx, entryID int64, names []string) error {
	ids, err := EnsureTagsTx(ctx, tx, names)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entry_tags WHERE entry_id = ?`, entryID); err != nil {
		return fmt.Errorf("clear entry tags: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entry_tags (entry_id, tag_id) VALUES (?, ?)`, entryID, id); err != nil {
			return fmt.Errorf("tag entry: %w", err)
		}
	}
	return nil
}

// TagsForEntries loads the tags of many entries at once.
//
// One query for a page of entries rather than one per entry: the day view
// renders up to a few dozen, and a query each would be the classic N+1 that
// turns a fast screen into a slow one as soon as somebody has a real week.
func (db *DB) TagsForEntries(ctx context.Context, entryIDs []int64) (map[int64][]string, error) {
	if len(entryIDs) == 0 {
		return map[int64][]string{}, nil
	}

	args := make([]any, len(entryIDs))
	for i, id := range entryIDs {
		args[i] = id
	}
	rows, err := db.read.QueryContext(ctx, `
		SELECT et.entry_id, t.name
		FROM entry_tags et
		JOIN tags t ON t.id = et.tag_id
		WHERE et.entry_id IN (`+repeatPlaceholders(len(entryIDs))+`)
		ORDER BY t.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("load entry tags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byEntry := make(map[int64][]string, len(entryIDs))
	for rows.Next() {
		var entryID int64
		var name string
		if err := rows.Scan(&entryID, &name); err != nil {
			return nil, err
		}
		byEntry[entryID] = append(byEntry[entryID], name)
	}
	return byEntry, rows.Err()
}

// ListTags returns every tag with how many entries carry it.
//
// The count is what makes the management screen useful: a tag on nothing is
// either a typo or something that has served its purpose, and both are worth
// seeing.
func (db *DB) ListTags(ctx context.Context) ([]domain.Tag, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT t.id, t.name, t.colour_key, t.created_at,
		       (SELECT COUNT(*) FROM entry_tags et WHERE et.tag_id = t.id)
		FROM tags t
		ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tags []domain.Tag
	for rows.Next() {
		var tag domain.Tag
		var createdAt string
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.ColourKey, &createdAt, &tag.EntryCount); err != nil {
			return nil, err
		}
		if tag.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

// UpdateTag renames a tag or recolours it.
func (db *DB) UpdateTag(ctx context.Context, tag domain.Tag) error {
	res, err := db.write.ExecContext(ctx,
		`UPDATE tags SET name = ?, colour_key = ? WHERE id = ?`,
		domain.NormaliseTag(tag.Name), tag.ColourKey, tag.ID)
	if err != nil {
		return fmt.Errorf("update tag: %w", err)
	}
	return requireOneRow(res)
}

// DeleteTag removes a tag and its links.
//
// Unlike the catalogue, a tag can be deleted: nothing is invoiced against it,
// so removing one loses a label rather than orphaning history. The links go
// with it through the foreign key's cascade.
func (db *DB) DeleteTag(ctx context.Context, id int64) error {
	res, err := db.write.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	return requireOneRow(res)
}

// tagFilterCondition builds the SQL that narrows a listing to entries carrying
// tags.
//
// All of them rather than any: filtering by #incident and #billable-review means
// entries that are both, which is what somebody looking for a specific slice
// expects. "Any" is available by filtering twice.
func tagFilterCondition(tags []string) (string, []any) {
	tags = domain.NormaliseTags(tags)
	if len(tags) == 0 {
		return "", nil
	}
	args := make([]any, 0, len(tags)+1)
	for _, name := range tags {
		args = append(args, name)
	}
	args = append(args, len(tags))

	return `e.id IN (
		SELECT et.entry_id FROM entry_tags et
		JOIN tags t ON t.id = et.tag_id
		WHERE t.name IN (` + repeatPlaceholders(len(tags)) + `)
		GROUP BY et.entry_id
		HAVING COUNT(DISTINCT t.id) = ?)`, args
}

// tagNamesOf is a small helper for building filters from free text.
func tagNamesOf(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return domain.ParseTagList(raw)
}
