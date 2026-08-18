-- 0006_customer_rate_rules.sql - customer-specific overtime, travel and
-- reimbursement rules.
--
-- The base hourly rate answers "what is an hour worth". It does not answer the
-- three questions every consulting contract also settles:
--
--   * what is an hour worth when it is the tenth one that day
--   * what is an hour worth when it is spent on a train rather than on the work
--   * what gets paid back, at what markup, and against what evidence
--
-- These are per-customer because they are contract terms, and every customer's
-- contract is different. They are all optional: a customer with none of them set
-- behaves exactly as before.
--
-- Why a `kind` on the entry rather than derived thresholds:
-- whether a particular hour is billable as overtime is a contractual judgement,
-- not something a tool should infer and silently invoice. The thresholds below
-- drive a *prompt* - "Tuesday has 9h 30m and none of it is marked overtime" -
-- and the person decides. Silently billing hour nine at 1.5x because somebody
-- forgot to stop a timer is how invoice disputes start.
-- See docs/adr/0024-customer-rate-rules.md.

-- ------------------------------------------------------------- time entries --

-- What sort of time this is. 'work' is the default and is what every existing
-- row becomes, so no history changes meaning.
ALTER TABLE time_entries ADD COLUMN kind TEXT NOT NULL DEFAULT 'work';

CREATE INDEX idx_entries_kind ON time_entries(kind) WHERE kind <> 'work';

-- ----------------------------------------------------------------- overtime --

-- Absolute overtime rate in minor units. 0 means "not set"; the multiplier is
-- then consulted. An absolute rate wins because a contract that names one is
-- naming it instead of a multiple, not as well as.
ALTER TABLE customers ADD COLUMN overtime_rate_minor INTEGER NOT NULL DEFAULT 0;

-- Overtime as a percentage of the resolved base rate: 150 = time and a half,
-- 200 = double time. 0 means "not set", so overtime bills at the base rate.
ALTER TABLE customers ADD COLUMN overtime_multiplier_pct INTEGER NOT NULL DEFAULT 0;

-- Thresholds for the prompt only, in seconds. 0 disables that prompt.
ALTER TABLE customers ADD COLUMN overtime_daily_threshold_seconds  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN overtime_weekly_threshold_seconds INTEGER NOT NULL DEFAULT 0;

-- ------------------------------------------------------------- travel time --

-- How travel time is billed for this customer:
--   ''         inherit - travel bills exactly like work
--   'rate'     use travel_rate_minor, or travel_multiplier_pct of the base rate
--   'unbilled' travel is recorded in full but never invoiced
--
-- 'unbilled' is a separate state rather than a 0% multiplier because the two
-- say different things to whoever reads the timesheet later, and because 0 is
-- already how these columns spell "not set".
ALTER TABLE customers ADD COLUMN travel_billing TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN travel_rate_minor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN travel_multiplier_pct INTEGER NOT NULL DEFAULT 0;

-- --------------------------------------------------------- reimbursement ----

-- Default markup applied to the billable side of an expense for this customer.
-- 0 means at cost, which is also the sensible default.
ALTER TABLE customers ADD COLUMN expense_markup_pct INTEGER NOT NULL DEFAULT 0;

-- Whether an expense for this customer is billable to them by default. Stored
-- as a string so that "not set" is distinguishable from "no".
--   ''    inherit - billable, the common case
--   'no'  expenses for this customer are reimbursed to the employee but never
--         invoiced to the customer
ALTER TABLE customers ADD COLUMN expense_billing TEXT NOT NULL DEFAULT '';

-- Reimbursement per unit, in minor units. Mileage is per kilometre, per diem is
-- per day. 0 means the customer has no such rule and the amount is typed by hand.
ALTER TABLE customers ADD COLUMN mileage_rate_minor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN per_diem_minor     INTEGER NOT NULL DEFAULT 0;

-- Above this amount a receipt must be attached. 0 disables the requirement.
--
-- It cannot be enforced when the expense is created - an attachment needs an
-- expense to belong to, so refusing to create one without a receipt would make
-- the receipt impossible to add. It is therefore a visible warning on the
-- expense, and a hard refusal at the point the claim is actually made: a week
-- cannot be submitted while an expense in it is missing evidence the contract
-- requires.
ALTER TABLE customers ADD COLUMN receipt_required_above_minor INTEGER NOT NULL DEFAULT 0;

-- --------------------------------------------------------------- expenses ---

-- Quantity-priced expenses: 42.5 km at 2.50/km, or 3 days at 260.00/day.
--
-- The quantity is in thousandths so that a distance can be exact without a
-- float ever touching a persisted field (docs/adr/0014-exact-money-and-duration.md).
-- unit is '' for an ordinary typed amount, 'km' or 'day' otherwise.
ALTER TABLE expenses ADD COLUMN quantity_milli  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE expenses ADD COLUMN unit            TEXT    NOT NULL DEFAULT '';
ALTER TABLE expenses ADD COLUMN unit_rate_minor INTEGER NOT NULL DEFAULT 0;
