-- 0013_reminders.sql - nudges, and the record of having waved one away.
--
-- A reminder here is not a message. Nothing is queued, sent or delivered: there
-- is no mail, no scheduler and no background job in this application, and adding
-- one for this would be the largest new dependency in the tree
-- (docs/adr/0034-reminders-are-shown-not-sent.md). A reminder is a statement
-- about the state of the timesheet right now, computed when a screen renders and
-- gone the moment it stops being true.
--
-- Which is why the only thing stored is the dismissal. "I know, and I do not
-- want to be told again today" is the one fact that cannot be derived from the
-- timesheet - everything else already can be.
--
-- Scope is the day or the week the dismissal applies to, as a date, so waving
-- away today's nudge says nothing about tomorrow's. It is the reason this is a
-- table rather than a column: a per-user flag would either come back too soon or
-- never come back at all.

CREATE TABLE reminder_dismissals (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    -- Which nudge: 'running_timers' | 'empty_day' | 'pending_proposals' |
    -- 'unsubmitted_week'.
    kind         TEXT    NOT NULL,
    -- The day or week start this applies to, as YYYY-MM-DD.
    scope        TEXT    NOT NULL,
    dismissed_at TEXT    NOT NULL
);

-- One dismissal per person per nudge per day. The unique index is what makes
-- dismissing twice - two tabs, a double click - one row rather than two.
CREATE UNIQUE INDEX idx_reminder_dismissed
    ON reminder_dismissals(user_id, kind, scope);

-- Whether to nudge at all, and from what hour of the local day. Sixteen because
-- a nudge about an unfinished day has to arrive while the day can still be
-- finished; one at half past five is a complaint rather than a reminder.
ALTER TABLE settings ADD COLUMN reminders_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE settings ADD COLUMN reminder_hour     INTEGER NOT NULL DEFAULT 16;
