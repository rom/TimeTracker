-- 0012_idle_observations.sql - what the application saw, not what it concluded.
--
-- A timer left running through lunch is the second failure mode of any tracker,
-- after one left running overnight. The overnight case is already handled by
-- max_timer_seconds, which flags what is obviously wrong. Lunch is not
-- obviously wrong: six hours is a plausible morning.
--
-- A web page cannot see whether you were working. It can see two things about
-- itself, and this table stores those and nothing else:
--
--   'asleep'    - the page's own clock jumped, so the machine slept or the
--                 browser suspended the tab. Wall-clock time passed with
--                 nothing running.
--   'untouched' - the page was visible and running the whole time, and saw no
--                 pointer, key or scroll event.
--
-- Both are observations about a browser tab, which is why the column is named
-- for the source rather than for a conclusion, and why nothing in the schema
-- says "idle time" as though the application knew. What the application knows is
-- that it saw nothing. Whether that stretch was work is the person's to say, and
-- resolved_at records that they said it.
--
-- See docs/adr/0033-idle-time-is-observed.md.

CREATE TABLE idle_observations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id   INTEGER NOT NULL REFERENCES time_entries(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    -- The observed stretch, clamped on the way in to lie within the entry.
    started_at TEXT    NOT NULL,
    ended_at   TEXT    NOT NULL,
    -- 'asleep' | 'untouched'.
    source     TEXT    NOT NULL,
    -- 'keep' | 'discard' | 'split' once a person has decided; empty until then.
    resolution TEXT    NOT NULL DEFAULT '',
    resolved_at TEXT   NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL
);

-- The question every screen asks: what has this person not decided about yet?
-- Partial on the unresolved rows, and the predicate is the one the query states
-- (ADR-0032) - resolved observations are kept for the audit trail and are dead
-- weight to this index.
CREATE INDEX idx_idle_unresolved ON idle_observations(user_id, started_at)
    WHERE resolution = '';

-- Resolving one asks for the observations of a single entry.
CREATE INDEX idx_idle_entry ON idle_observations(entry_id);

-- How long a stretch has to be before the page reports it, and the switch that
-- turns the whole thing off. Zero means off, as with max_timer_seconds.
--
-- Fifteen minutes: long enough that a coffee is not a prompt, short enough that
-- a lunch is. It has to be well clear of the few minutes a browser takes to
-- throttle a background tab, because a throttled tab and a sleeping machine look
-- identical from inside the page.
ALTER TABLE settings ADD COLUMN idle_seconds INTEGER NOT NULL DEFAULT 900;
