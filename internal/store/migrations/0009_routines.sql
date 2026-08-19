-- 0009_routines.sql - recurring entries.
--
-- Lunch, the Monday stand-up, Friday admin, the weekly project meeting. Time
-- that happens on a schedule, is the same every week, and is tedious enough to
-- type that people stop bothering - which is how a timesheet quietly starts
-- under-reporting.
--
-- A routine is a *template*, not a schedule that fires. Nothing is created
-- until somebody says so. Auto-generating billable hours because the calendar
-- said Tuesday would put time on an invoice that nobody did, and the first
-- person to notice would be the client. The day view offers what is due and it
-- takes one click; that is the whole feature.
--
-- See docs/adr/0027-routines-are-offered-not-fired.md.

CREATE TABLE routines (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    assignment_id INTEGER NOT NULL REFERENCES assignments(id),
    name          TEXT    NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',

    -- Which days it applies to, as ISO weekday numbers joined by commas:
    -- "1,2,3,4,5" is weekdays, "1" is Mondays. A string rather than a bitmask
    -- because it is readable in a database browser and in a backup, and this is
    -- not a column anything joins on.
    weekdays TEXT NOT NULL DEFAULT '1,2,3,4,5',

    -- What it records. A start time of '' means "no particular time", and the
    -- entry is placed at the start of the working day like any other entry with
    -- no time of its own.
    start_time      TEXT    NOT NULL DEFAULT '',
    duration_seconds INTEGER NOT NULL,
    billable        INTEGER NOT NULL DEFAULT 0,
    kind            TEXT    NOT NULL DEFAULT 'work',
    -- Tags to apply, comma separated, in the same normalised form the tag table
    -- stores. Denormalised deliberately: a routine is a template, and templates
    -- referencing tag ids would break when a tag is renamed.
    tags TEXT NOT NULL DEFAULT '',

    active     INTEGER NOT NULL DEFAULT 1,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

-- The day view asks "what does this person have due today", on every render.
CREATE INDEX idx_routines_user ON routines(user_id, active, sort_order);
