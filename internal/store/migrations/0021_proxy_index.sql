-- 0021_proxy_index.sql - the inbox's own index.
--
-- Found by the performance probe added beside it, which is the point of having
-- one: "entries this person recorded for somebody else" had no index to seek
-- and walked the whole table. Measured at 124 microseconds against a hundred
-- entries and 1.9 milliseconds against twenty thousand - fifteen times the
-- work for two hundred times the data, which is the signature of a scan that
-- stops early rather than one that does not stop at all, because the query
-- takes a limit.
--
-- Partial, on the condition the query actually writes. Proxy entries are a
-- small fraction of any instance's time - most people record their own - so an
-- index over all of time_entries would be mostly rows this never looks at. The
-- predicate has to be written exactly as the query writes it, or SQLite will
-- not use the index: this is the same lesson 0011 records about the planner
-- needing an index that matches rather than a planner that is clever.
CREATE INDEX idx_entries_proxy ON time_entries(entered_by, started_at DESC)
    WHERE user_id != entered_by;
