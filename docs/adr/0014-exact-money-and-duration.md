# ADR-0014: Integer minor units and whole seconds

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-014, ASR-007

## Context

This application computes amounts that go on invoices. Representing money as
`float64` is the classic defect that passes every test written by the person who
wrote the code, and then surfaces as a one-cent discrepancy on a client's invoice —
because 0.1 is not representable in binary floating point, and a few thousand
additions accumulate the error.

Durations have a parallel problem: storing hours as `1.75` and multiplying by a rate
produces a value whose last digit depends on evaluation order.

There is also a policy question that gets confused with the representation one:
consultancies commonly bill in increments (round each entry up to the next 15
minutes, or apply a one-hour minimum). Where in the pipeline that rounding happens
changes the invoice total, so it must be a decision rather than an accident.

## Decision

**Representation**

* **Money** is an integer count of **minor units** (cents, öre, pence) plus an
  explicit **currency code**. There is a `Money` type; it is never a bare int, and
  it refuses to add two different currencies. Rates are stored as minor units per
  hour.
* **Durations** are whole **seconds**, as `int64`.
* **No `float64` appears in any persisted field, any total, or any export.** Percent
  values for display are computed at the point of rendering and never fed back into
  a calculation.

**Rounding**

* Rounding is applied at exactly **one** documented point: when an entry's billable
  duration is derived, before it is multiplied by a rate. Totals are the sum of
  already-rounded per-entry amounts, never a rounding of a sum — the two differ, and
  the former is what a client can reconcile line by line on the invoice.
* The rule (increment, direction, minimum) is configurable per project, falling back
  to per-customer then global, and the rule that was applied is **recorded on the
  entry**, so re-running a historical report cannot change a number that has already
  been invoiced.
* Multiplication of a rate by a duration rounds **half away from zero** at the last
  minor unit, matching the convention people expect from invoices; the rule is
  stated in the code and covered by boundary tests.

Exports carry both the raw and the billable duration, plus currency and rounding
rule, so a downstream invoicing system can verify our arithmetic rather than trust
it.

## Consequences

**Positive**

* Totals are exact and reproducible. A report re-run in a year produces the number
  that was invoiced.
* Currency mistakes become type errors instead of silently wrong sums.
* The rounding policy is visible on the record, so a client asking "why is this 15
  minutes?" gets an answer.

**Negative / accepted costs**

* More ceremony than `float64`: every calculation goes through the `Money` type, and
  display formatting is explicit per currency.
* Recording the rounding rule per entry adds columns and means a policy change does
  not retroactively alter history — correct, but occasionally surprising to a user
  who expected it to.
* Integer minor units cannot express sub-cent rates (e.g. fractional per-unit
  pricing). Not needed for hourly billing; would require a new ADR.
* No automatic currency conversion; a multi-currency report totals per currency
  rather than producing one figure.

## Alternatives considered

**`float64`** — least code. Rejected on ASR-014; the failure is invisible until it
is in front of a client.

**Arbitrary-precision decimal (`shopspring/decimal`)** — correct, and it handles
sub-unit rates. Rejected as heavier than needed: it invites the same "where does
rounding happen" ambiguity, and integer minor units make the answer structural.

**Store durations as minutes** — smaller numbers, and billing is never sub-minute.
Rejected: a timer records a real start and stop, and truncating to minutes at
capture loses information we might want to reconstruct later.

## Related

* ADR-0007 (exports), ADR-0015 (time zones)
