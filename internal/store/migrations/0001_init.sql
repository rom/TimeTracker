-- 0001_init.sql - the initial schema.
--
-- Conventions used throughout, and the reasoning behind them:
--
--   * Timestamps are TEXT in ISO-8601 with an explicit offset, always UTC.
--     SQLite has no timestamp type; storing a string keeps the value unambiguous,
--     sortable lexicographically, and readable by any external tool.
--     See docs/adr/0015-utc-storage-local-display.md.
--   * Durations are INTEGER seconds; money is INTEGER minor units with a separate
--     currency column. No REAL columns exist anywhere in this schema, by design.
--     See docs/adr/0014-exact-money-and-duration.md.
--   * Catalogue rows (customer, project, assignment) are archived, never deleted,
--     because deleting one would orphan invoiced history.
--   * Colours are stored as palette *keys*, not hex values, so an entity stays
--     legible in all seven themes.
--     See docs/adr/0011-theming-via-css-custom-properties.md.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name  TEXT    NOT NULL,
    email         TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL DEFAULT 'member',
    -- IANA zone name. Decides which calendar day an entry belongs to.
    time_zone     TEXT    NOT NULL DEFAULT 'UTC',
    theme         TEXT    NOT NULL DEFAULT 'light',
    active        INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT    NOT NULL
);

-- Email is optional in local mode, so uniqueness is enforced only over the rows
-- that actually have one.
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE email != '';

CREATE TABLE customers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    code        TEXT    NOT NULL DEFAULT '',
    currency    TEXT    NOT NULL DEFAULT '',
    colour_key  TEXT    NOT NULL DEFAULT 'slate',
    icon        TEXT    NOT NULL DEFAULT '',
    notes       TEXT    NOT NULL DEFAULT '',
    -- Optional default hourly rate in minor units; 0 means "no default".
    rate_minor  INTEGER NOT NULL DEFAULT 0,
    archived_at TEXT,
    created_at  TEXT    NOT NULL
);

CREATE INDEX idx_customers_archived ON customers(archived_at);

CREATE TABLE projects (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_id      INTEGER NOT NULL REFERENCES customers(id),
    name             TEXT    NOT NULL,
    code             TEXT    NOT NULL DEFAULT '',
    colour_key       TEXT    NOT NULL DEFAULT 'slate',
    icon             TEXT    NOT NULL DEFAULT '',
    billable_default INTEGER NOT NULL DEFAULT 1,
    rate_minor       INTEGER NOT NULL DEFAULT 0,
    -- Serialised domain.RoundingRule, e.g. 'up/900/0'. Empty means inherit.
    rounding_rule    TEXT    NOT NULL DEFAULT '',
    budget_seconds   INTEGER NOT NULL DEFAULT 0,
    budget_minor     INTEGER NOT NULL DEFAULT 0,
    archived_at      TEXT,
    created_at       TEXT    NOT NULL
);

CREATE INDEX idx_projects_customer ON projects(customer_id);

CREATE TABLE assignments (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       INTEGER NOT NULL REFERENCES projects(id),
    name             TEXT    NOT NULL,
    code             TEXT    NOT NULL DEFAULT '',
    colour_key       TEXT    NOT NULL DEFAULT 'slate',
    icon             TEXT    NOT NULL DEFAULT '',
    billable_default INTEGER NOT NULL DEFAULT 1,
    rate_minor       INTEGER NOT NULL DEFAULT 0,
    sort_order       INTEGER NOT NULL DEFAULT 0,
    favourite        INTEGER NOT NULL DEFAULT 0,
    archived_at      TEXT,
    created_at       TEXT    NOT NULL
);

CREATE INDEX idx_assignments_project ON assignments(project_id);

CREATE TABLE time_entries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Whose time this is.
    user_id       INTEGER NOT NULL REFERENCES users(id),
    -- Who recorded it. Differs from user_id for a proxy entry, and both are
    -- always kept: see docs/adr/0005-proxy-time-entry.md.
    entered_by    INTEGER NOT NULL REFERENCES users(id),
    assignment_id INTEGER NOT NULL REFERENCES assignments(id),
    started_at    TEXT    NOT NULL,
    -- NULL means the timer is still running. There is deliberately NO unique
    -- constraint limiting a user to one running entry, and overlapping intervals
    -- are legal: see docs/adr/0004-concurrent-timers.md.
    ended_at      TEXT,
    -- Derived from the interval and stored so reporting can sum in SQL.
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    note          TEXT    NOT NULL DEFAULT '',
    billable      INTEGER NOT NULL DEFAULT 1,
    -- 'confirmed' | 'pending' | 'rejected'. Only 'confirmed' counts.
    status        TEXT    NOT NULL DEFAULT 'confirmed',
    time_zone     TEXT    NOT NULL DEFAULT 'UTC',
    -- The billing snapshot, written when the entry is billed and never
    -- recomputed, so an invoiced amount cannot change under a later rate change.
    rounding_rule_applied TEXT    NOT NULL DEFAULT '',
    billable_seconds      INTEGER NOT NULL DEFAULT 0,
    rate_minor            INTEGER NOT NULL DEFAULT 0,
    amount_minor          INTEGER NOT NULL DEFAULT 0,
    currency              TEXT    NOT NULL DEFAULT '',
    -- Needs human review (e.g. a timer left running past the maximum). Flagged
    -- entries are excluded from totals until resolved.
    flagged       INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

-- The day and week views filter by user and an interval of started_at; this is
-- the index that keeps them inside the ASR-012 budget.
CREATE INDEX idx_entries_user_started ON time_entries(user_id, started_at);
-- Project and customer reports walk entries by assignment over a range.
CREATE INDEX idx_entries_assignment_started ON time_entries(assignment_id, started_at);
-- The running-timer header is rendered on every page, so the lookup it performs
-- gets a partial index covering exactly the few rows that are ever running.
CREATE INDEX idx_entries_running ON time_entries(user_id) WHERE ended_at IS NULL;
-- The proxy inbox lists entries awaiting a decision.
CREATE INDEX idx_entries_status ON time_entries(user_id, status) WHERE status != 'confirmed';

CREATE TABLE tags (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    colour_key TEXT    NOT NULL DEFAULT 'slate',
    created_at TEXT    NOT NULL
);

CREATE TABLE entry_tags (
    entry_id INTEGER NOT NULL REFERENCES time_entries(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, tag_id)
);

CREATE INDEX idx_entry_tags_tag ON entry_tags(tag_id);

-- Append-only. Nothing in the application updates or deletes a row here, and the
-- audit row is written in the same transaction as the change it describes, so no
-- change can exist without its record.
-- See docs/adr/0010-audit-log-and-rsyslog.md.
CREATE TABLE audit_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TEXT    NOT NULL,
    actor_id      INTEGER NOT NULL DEFAULT 0,
    actor_name    TEXT    NOT NULL DEFAULT '',
    -- Set when the actor acted on behalf of someone else.
    on_behalf_of  INTEGER NOT NULL DEFAULT 0,
    action        TEXT    NOT NULL,
    resource_type TEXT    NOT NULL,
    resource_id   INTEGER NOT NULL DEFAULT 0,
    -- Compact JSON description of what changed.
    detail        TEXT    NOT NULL DEFAULT '',
    ip            TEXT    NOT NULL DEFAULT '',
    request_id    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_resource ON audit_events(resource_type, resource_id, at);
CREATE INDEX idx_audit_at ON audit_events(at);

-- Single-row table holding instance-wide defaults. The CHECK constraint is what
-- keeps it single-row.
CREATE TABLE settings (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    default_currency TEXT    NOT NULL DEFAULT 'EUR',
    default_rounding TEXT    NOT NULL DEFAULT 'none',
    default_rate_minor INTEGER NOT NULL DEFAULT 0,
    -- ISO-8601 week start: 1 = Monday.
    week_start       INTEGER NOT NULL DEFAULT 1,
    -- A timer running longer than this is flagged for review rather than counted.
    max_timer_seconds INTEGER NOT NULL DEFAULT 43200
);

INSERT INTO settings (id) VALUES (1);
