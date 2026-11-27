-- 0020_billing_runs.sql - what has been billed.
--
-- The money layer computed an exact figure for every entry and froze it
-- (docs/adr/0014-exact-money-and-duration.md), reports could total it, exports
-- could itemise it - and nothing anywhere recorded that an invoice had been
-- sent. The application could say what an hour was worth and could not say
-- whether anybody had been charged for it. So it could not answer "what is
-- still unbilled", which is the question the whole money layer exists to serve,
-- and nothing stopped the same fortnight going out twice.
--
-- A run rather than a flag on each entry. A flag would say "this was billed"
-- and nothing else: correcting an invoice would mean finding its entries by
-- hand, and there would be no record of what any one invoice contained. A run
-- is the invoice's shadow here - which customer, which days, what it came to -
-- and the entries point at it. Unbilled is then "belongs to no run", which is a
-- query rather than a convention.
--
-- The totals are copied onto the run rather than derived from the entries it
-- holds, for the reason the entry's own snapshot exists: an invoice is a
-- statement about a moment, and a rate that changes next quarter must not
-- change what last quarter's invoice said.
--
-- Voiding rather than deleting, because an invoice is sometimes cancelled and
-- the fact that it existed is part of the account's history. A voided run
-- releases its entries, which become unbilled again and can go on the next one.
CREATE TABLE billing_runs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_id      INTEGER NOT NULL REFERENCES customers(id),
    starts_on        TEXT    NOT NULL,
    ends_on          TEXT    NOT NULL,
    -- The invoice number in whatever system actually issues invoices. Free
    -- text: this application does not number invoices and should not pretend
    -- to (DESIGN.md §11 puts invoicing out of scope).
    reference        TEXT    NOT NULL DEFAULT '',
    note             TEXT    NOT NULL DEFAULT '',
    currency         TEXT    NOT NULL DEFAULT '',
    entry_count      INTEGER NOT NULL DEFAULT 0,
    billable_seconds INTEGER NOT NULL DEFAULT 0,
    amount_minor     INTEGER NOT NULL DEFAULT 0,
    created_by       INTEGER NOT NULL,
    created_at       TEXT    NOT NULL,
    -- Empty until voided. A date rather than a flag, because "when was this
    -- cancelled" is the next question after "was it".
    voided_at        TEXT    NOT NULL DEFAULT '',
    voided_by        INTEGER NOT NULL DEFAULT 0
);

-- Zero means unbilled, the same convention the rest of the schema uses for an
-- absent reference.
ALTER TABLE time_entries ADD COLUMN billing_run_id INTEGER NOT NULL DEFAULT 0;

-- The index the unbilled query needs: "entries of this customer, in this
-- range, belonging to no run".
CREATE INDEX idx_entries_billing_run ON time_entries(billing_run_id);
CREATE INDEX idx_billing_runs_customer ON billing_runs(customer_id, starts_on);
