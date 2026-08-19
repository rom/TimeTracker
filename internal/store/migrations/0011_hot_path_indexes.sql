-- 0011_hot_path_indexes.sql - indexes for the queries every page render runs.
--
-- Written after measuring, not before. The ASR-012 suite found the day view at
-- 365 ms against a hundred thousand entries where the budget is 100, and - the
-- detail that pointed at the cause - a *one-year report* at 31 ms. A query
-- covering a year cannot be ten times faster than one covering a day unless the
-- day view is doing something unrelated to the day, and it was: the page shell
-- ran two queries that walked every entry the user had.
--
-- The indexes to serve them already existed. They were not being used.
--
--   idx_entries_status ON time_entries(user_id, status) WHERE status != 'confirmed'
--
-- The inbox asks for `status IN ('pending')`. That implies `status != 'confirmed'`
-- to a reader, and SQLite's partial-index matcher does not make the inference, so
-- it fell back to idx_entries_user_started and walked the lot. The fix is an
-- index whose predicate is exactly the condition the query states.
--
-- ANALYZE was tried first and rejected: it moved the planner onto the right
-- index for running timers (95 ms to 0.4 ms) and onto a worse one for the inbox
-- (98 ms to 320 ms). Statistics make the planner's choice depend on the shape of
-- the data, which is precisely the sort of thing that is fine in testing and
-- surprising in production. An index that matches the query exactly does not
-- need the planner to be clever.

-- Pending work awaiting a decision. The predicate is the one the query writes.
CREATE INDEX idx_entries_pending ON time_entries(user_id)
    WHERE status = 'pending';
CREATE INDEX idx_expenses_pending ON expenses(user_id)
    WHERE status = 'pending';

-- Recently used assignments, for the one-click list on the day screen.
--
-- The query groups a user's entries by assignment to find the last time each was
-- used. Grouping three years of history to rank eight assignments cost 170 ms;
-- the service now bounds it to a recent window, and this index makes that window
-- a range scan that stops rather than a scan that finishes.
--
-- Descending on started_at, because the query wants the newest first and a
-- descending index lets it stop at the limit instead of sorting what it found.
CREATE INDEX idx_entries_recent ON time_entries(user_id, started_at DESC, assignment_id);
