-- 0018_absence.sql - the days somebody was not working.
--
-- A timesheet that cannot record a holiday makes people do one of two things:
-- leave the week empty, or invent a customer called "Internal" to book leave
-- against. The first is worse than it sounds, because the application then
-- nags about it - the empty-day reminder and the unaccounted-time banner both
-- read a week off as a failure to fill the timesheet in. The weekend work made
-- the gap plain: the instance now knows that Saturdays are not worked and has
-- never known anything about the 25th of December.
--
-- Its own record rather than a kind of time entry. An entry needs an
-- assignment, which needs a project, which needs a customer, so recording a
-- holiday as time would mean a fake customer in every instance's catalogue and
-- leave appearing in reports grouped by customer. Absence is about a person and
-- a date, and nothing else, so it is stored as a person and a date and nothing
-- else.
--
-- user_id of 0 is the instance's own: a public holiday, which applies to
-- everybody and is set once by an administrator. The same convention the
-- recycle bin uses for records that belong to the instance rather than to a
-- person.
--
-- Dates are inclusive at both ends, and stored as YYYY-MM-DD rather than as
-- instants. A day off is a day in the calendar somebody lives in, not an
-- interval on a clock: it does not start at midnight UTC, and storing it as a
-- timestamp would make a holiday in Auckland begin on the wrong day.
CREATE TABLE absences (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 0 for a public holiday, which is everybody's.
    user_id    INTEGER NOT NULL DEFAULT 0,
    -- 'holiday' | 'sick' | 'public' | 'parental' | 'other'
    kind       TEXT    NOT NULL,
    -- Inclusive, both ends.
    starts_on  TEXT    NOT NULL,
    ends_on    TEXT    NOT NULL,
    note       TEXT    NOT NULL DEFAULT '',
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT    NOT NULL
);

-- The question asked of this table is always "what covers this range, for this
-- person or for everybody", so that is what the index serves.
CREATE INDEX idx_absences_lookup ON absences(user_id, starts_on, ends_on);
