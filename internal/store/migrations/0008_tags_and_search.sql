-- 0008_tags_and_search.sql - tags become usable, and entries become searchable.
--
-- The `tags` and `entry_tags` tables have existed since 0001 and have never had
-- a row in them: quick-add parsed `#travel` out of the line and threw it away,
-- and the architecture listed tags as designed but not implemented. The tables
-- were right, so they stay; what was missing was everything around them.
--
-- Two additions here:
--
--   * an index for the other direction. 0001 indexed entry_tags(tag_id), which
--     serves "which entries carry this tag". Rendering a page of entries asks
--     the opposite question for a list of entry ids at once, and that had no
--     index at all.
--
--   * a full-text index, below.
--
-- A tag is a cross-cutting label: #incident, #onboarding, #billable-review. It
-- deliberately does not replace the customer/project/assignment hierarchy, which
-- answers "who is invoiced". A tag answers "what sort of work was this", which
-- cuts across every level of that hierarchy and belongs to none of it.

-- 0001 has entry_tags(tag_id) for tag -> entries. This is entry -> tags, which
-- is what every rendered page needs and what the page-at-a-time load uses.
CREATE INDEX idx_entry_tags_entry ON entry_tags(entry_id, tag_id);

-- ------------------------------------------------------------- searching ---

-- A full-text index over the words an entry can be found by: its note, the
-- assignment, project and customer it is on, and its tags.
--
-- Trigram rather than the default tokenizer, because the search people
-- actually want here is substring: "redir" should find "login redirect", and a
-- word-boundary tokenizer will not. Trigram is what makes `LIKE '%…%'` fast
-- enough to be the primary path rather than a fallback.
--
-- The trade-off is size - a trigram index is several times the text it covers -
-- and a two-character minimum, since a query shorter than a trigram cannot be
-- looked up. Both are stated in the search code, which falls back to LIKE for
-- short queries rather than returning nothing.
--
-- This is an external-content-free table: the rows are written by the
-- application rather than mirrored automatically, because the searchable text
-- spans four joined tables and no single trigger could keep it right.
CREATE VIRTUAL TABLE entry_search USING fts5(
    note,
    assignment,
    project,
    customer,
    tags,
    tokenize = 'trigram'
);

-- The index is keyed by rowid = time_entries.id, so a hit maps straight back to
-- an entry with no extra column.
