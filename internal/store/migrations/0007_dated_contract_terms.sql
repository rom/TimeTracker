-- 0007_dated_contract_terms.sql - contract terms become dated, and can be set
-- per project as well as per customer.
--
-- ADR-0024 shipped the terms as columns on the customer and named two costs it
-- was accepting: no history, and one set of terms per customer. Both have now
-- been paid for. A contract's terms change on renewal, and an engagement can
-- agree different overtime for one project than for the rest of the account.
--
-- Two changes, one table:
--
--   * terms carry an `effective_from` date, and the row that applies to an
--     entry is the latest one on or before the day that entry belongs to. Old
--     entries keep their frozen amounts either way (ADR-0014), but a *new*
--     entry backdated into last year now prices at last year's terms.
--
--   * terms attach to a customer or to a project. A project's terms are merged
--     over its customer's field by field, so a project that differs only in
--     overtime says only that, and the rest keeps following the account. This
--     mirrors how rates already inherit.
--
-- See docs/adr/0026-dated-contract-terms.md.

CREATE TABLE contract_terms (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 'customer' or 'project'. Not two nullable foreign keys: a row that is
    -- somehow both, or neither, would have no meaning and no error.
    scope    TEXT    NOT NULL,
    scope_id INTEGER NOT NULL,
    -- The first day these terms apply, YYYY-MM-DD. The empty string means
    -- "since forever", which is what the terms carried over from the customer
    -- columns get: they have always applied, because there was nothing else.
    effective_from TEXT NOT NULL DEFAULT '',

    overtime_rate_minor              INTEGER NOT NULL DEFAULT 0,
    overtime_multiplier_pct          INTEGER NOT NULL DEFAULT 0,
    overtime_daily_threshold_seconds INTEGER NOT NULL DEFAULT 0,
    overtime_weekly_threshold_seconds INTEGER NOT NULL DEFAULT 0,

    travel_billing        TEXT    NOT NULL DEFAULT '',
    travel_rate_minor     INTEGER NOT NULL DEFAULT 0,
    travel_multiplier_pct INTEGER NOT NULL DEFAULT 0,

    expense_markup_pct           INTEGER NOT NULL DEFAULT 0,
    expense_billing              TEXT    NOT NULL DEFAULT '',
    mileage_rate_minor           INTEGER NOT NULL DEFAULT 0,
    per_diem_minor               INTEGER NOT NULL DEFAULT 0,
    receipt_required_above_minor INTEGER NOT NULL DEFAULT 0,

    -- Why these terms changed. A rate that went up on renewal is explicable
    -- years later only if somebody wrote down why.
    note       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- One set of terms per scope per start date. Editing a date to collide with an
-- existing row is refused rather than silently replacing it.
CREATE UNIQUE INDEX idx_terms_scope_from
    ON contract_terms(scope, scope_id, effective_from);

-- The resolution query walks a scope's rows newest-first looking for the first
-- one on or before a date.
CREATE INDEX idx_terms_lookup ON contract_terms(scope, scope_id, effective_from DESC);

-- Carry the existing per-customer terms across, as terms that have always
-- applied. Only customers that actually have terms: a row of zeroes for every
-- customer would make "this customer has no terms" indistinguishable from
-- "this customer has terms that happen to be empty".
INSERT INTO contract_terms (
    scope, scope_id, effective_from,
    overtime_rate_minor, overtime_multiplier_pct,
    overtime_daily_threshold_seconds, overtime_weekly_threshold_seconds,
    travel_billing, travel_rate_minor, travel_multiplier_pct,
    expense_markup_pct, expense_billing, mileage_rate_minor, per_diem_minor,
    receipt_required_above_minor, note, created_at, updated_at)
SELECT 'customer', id, '',
       overtime_rate_minor, overtime_multiplier_pct,
       overtime_daily_threshold_seconds, overtime_weekly_threshold_seconds,
       travel_billing, travel_rate_minor, travel_multiplier_pct,
       expense_markup_pct, expense_billing, mileage_rate_minor, per_diem_minor,
       receipt_required_above_minor,
       'carried over when terms became dated',
       -- RFC 3339, matching store.formatTime. SQLite's datetime() separates the
       -- date and time with a space, which the reader refuses - and which no
       -- test on a fresh database would ever see, because a fresh database has
       -- no terms to carry over.
       strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
       strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
FROM customers
WHERE overtime_rate_minor <> 0
   OR overtime_multiplier_pct <> 0
   OR overtime_daily_threshold_seconds <> 0
   OR overtime_weekly_threshold_seconds <> 0
   OR travel_billing <> ''
   OR travel_rate_minor <> 0
   OR travel_multiplier_pct <> 0
   OR expense_markup_pct <> 0
   OR expense_billing <> ''
   OR mileage_rate_minor <> 0
   OR per_diem_minor <> 0
   OR receipt_required_above_minor <> 0;

-- And drop the columns, so there is exactly one place terms live. Keeping them
-- as a deprecated second copy is how the entry insert came to disagree with
-- itself; two sources of truth are worse than a migration.
ALTER TABLE customers DROP COLUMN overtime_rate_minor;
ALTER TABLE customers DROP COLUMN overtime_multiplier_pct;
ALTER TABLE customers DROP COLUMN overtime_daily_threshold_seconds;
ALTER TABLE customers DROP COLUMN overtime_weekly_threshold_seconds;
ALTER TABLE customers DROP COLUMN travel_billing;
ALTER TABLE customers DROP COLUMN travel_rate_minor;
ALTER TABLE customers DROP COLUMN travel_multiplier_pct;
ALTER TABLE customers DROP COLUMN expense_markup_pct;
ALTER TABLE customers DROP COLUMN expense_billing;
ALTER TABLE customers DROP COLUMN mileage_rate_minor;
ALTER TABLE customers DROP COLUMN per_diem_minor;
ALTER TABLE customers DROP COLUMN receipt_required_above_minor;
