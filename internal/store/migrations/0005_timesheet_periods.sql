-- 0005_timesheet_periods.sql - weekly submit and approve.
--
-- A timesheet period is one person's week. It exists so that hours can be
-- declared finished, checked by someone else, and then frozen - which is what
-- makes a timesheet an approved record rather than a working note.
--
-- The period is the unit of locking, not the individual entry. Locking entries
-- one by one would leave a week half-frozen, and "which of my hours can I still
-- correct?" would have no simple answer.

CREATE TABLE timesheet_periods (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The Monday (or whichever day the instance starts its week on) of the week
    -- this covers, as YYYY-MM-DD in the user's own zone. A date rather than an
    -- instant: a week is a calendar thing, and which week an entry belongs to
    -- is decided by its local day (docs/adr/0015-utc-storage-local-display.md).
    week_start  TEXT    NOT NULL,
    -- 'open' | 'submitted' | 'approved' | 'rejected'
    --
    -- A row exists only once something has happened to the week. A week with no
    -- row is open, which keeps the table small and means the feature costs
    -- nothing for anyone who never uses it.
    status      TEXT    NOT NULL DEFAULT 'open',
    submitted_at TEXT   NOT NULL DEFAULT '',
    -- Who decided, and when. A rejection keeps its reason for the same purpose a
    -- rejected proxy entry does: the person whose week it is needs to know what
    -- to fix.
    decided_by  INTEGER NOT NULL DEFAULT 0,
    decided_at  TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    -- Totals as they stood at submission, so an approval is a decision about
    -- specific figures rather than about whatever the week says today.
    submitted_seconds INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

-- One period per person per week. The unique index is what makes "submit"
-- idempotent rather than a way to create duplicates.
CREATE UNIQUE INDEX idx_periods_user_week ON timesheet_periods(user_id, week_start);
-- A manager's approval queue reads by status.
CREATE INDEX idx_periods_status ON timesheet_periods(status, week_start);
