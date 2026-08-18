-- 0004_expenses_attachments.sql - expenses, attachments, tags and settings.
--
-- Layers 3 and 4 of docs/MVP_PLAN.md: rich capture (ASR-013) and the proxy
-- confirmation workflow (ASR-008), which was designed into the time_entries
-- table from the start and only needs its supporting columns here.

CREATE TABLE expenses (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    entered_by  INTEGER NOT NULL REFERENCES users(id),
    project_id  INTEGER NOT NULL REFERENCES projects(id),
    -- The date the cost was incurred, in the user's local zone, as YYYY-MM-DD.
    -- A date rather than an instant: a receipt has a day, not a moment.
    spent_on    TEXT    NOT NULL,
    category    TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    -- Money in minor units with an explicit currency, as everywhere else.
    amount_minor INTEGER NOT NULL DEFAULT 0,
    currency     TEXT    NOT NULL DEFAULT '',
    -- Billable and reimbursable are INDEPENDENT questions, and the application
    -- never conflates them: a taxi paid by the employee and re-charged to the
    -- client is both; a hotel the client booked directly is neither.
    billable      INTEGER NOT NULL DEFAULT 0,
    reimbursable  INTEGER NOT NULL DEFAULT 0,
    -- Optional markup applied to a billable expense, in whole percent.
    markup_pct    INTEGER NOT NULL DEFAULT 0,
    -- The billed amount including markup, frozen when the expense is recorded
    -- for the same reason time entries freeze their rate.
    billed_minor  INTEGER NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL DEFAULT 'confirmed',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE INDEX idx_expenses_user_date    ON expenses(user_id, spent_on);
CREATE INDEX idx_expenses_project_date ON expenses(project_id, spent_on);
CREATE INDEX idx_expenses_status       ON expenses(user_id, status) WHERE status != 'confirmed';

-- Attachment metadata. The bytes live on disk, content-addressed by their
-- SHA-256, so an identical file attached twice is stored once.
-- See docs/adr/0013-attachment-storage.md.
CREATE TABLE attachments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    -- What this is attached to: 'time_entry' or 'expense'.
    owner_type  TEXT    NOT NULL,
    owner_id    INTEGER NOT NULL,
    -- The content hash, which is also the storage path. Several rows may share
    -- one hash; the blob is removed when the last reference goes.
    sha256      TEXT    NOT NULL,
    -- The name the user's file had. NEVER used as a path component: that is
    -- what removes traversal and case-collision problems in one move.
    filename    TEXT    NOT NULL,
    -- Determined by sniffing the content on the server, never taken from the
    -- client's claim.
    mime        TEXT    NOT NULL,
    size_bytes  INTEGER NOT NULL,
    uploaded_by INTEGER NOT NULL REFERENCES users(id),
    created_at  TEXT    NOT NULL
);

CREATE INDEX idx_attachments_owner ON attachments(owner_type, owner_id);
CREATE INDEX idx_attachments_hash  ON attachments(sha256);

-- The proxy workflow needs to record the subject's decision, not just its
-- outcome: "rejected, and here is why" is a different record from "absent".
ALTER TABLE time_entries ADD COLUMN decided_by  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE time_entries ADD COLUMN decided_at  TEXT    NOT NULL DEFAULT '';
ALTER TABLE time_entries ADD COLUMN decision_note TEXT  NOT NULL DEFAULT '';

-- Display preferences an administrator sets for the instance.
ALTER TABLE settings ADD COLUMN show_clock        INTEGER NOT NULL DEFAULT 1;
ALTER TABLE settings ADD COLUMN show_time_and_date INTEGER NOT NULL DEFAULT 1;
