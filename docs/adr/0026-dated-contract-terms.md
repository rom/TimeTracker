# ADR-0026: Contract terms are dated records, resolved per project

* **Status:** accepted
* **Date:** 2026-08-18
* **Supersedes the storage decision in:** [ADR-0024](0024-customer-rate-rules.md)
* **Addresses:** ASR-021

## Context

[ADR-0024](0024-customer-rate-rules.md) put overtime, travel and reimbursement
terms on the customer, as columns, and named two costs it was knowingly
accepting:

> **Rules live on the customer with no history.** Changing a multiplier changes
> what *future* entries are worth […] but there is no record in this table of
> what the terms used to be.
>
> **One set of terms per customer.** A customer with different overtime terms
> per project cannot express that.

Both have now been asked for, and both are ordinary rather than exotic.
Contracts are renegotiated on renewal, and "what were we charging in March"
has to be answerable from the application rather than from a backup. An account
with several engagements routinely agrees different overtime for one of them
while the rest follow the master agreement.

## Decision

**Terms become their own records, dated, attachable to a customer or a
project.**

```
contract_terms(scope, scope_id, effective_from, …rules…, note)
```

Resolution for an entry on day *D* is two steps:

1. the **latest** customer terms whose `effective_from` is on or before *D*;
2. the **latest** project terms likewise, **merged over** them field by field.

`effective_from` empty means "since forever", which is what the terms carried
over from the old columns receive — they have always applied, because there was
nothing else.

**The merge is field-level, not whole-record.** A project that differs only in
overtime says only that, and everything else keeps following the account. The
alternative — the most specific applicable record wins entirely — would force
every project to restate its customer's whole contract, and those copies would
drift the moment one of them was renegotiated. Field-level inheritance is also
what the base rate already does, so there is one mental model rather than two.

**That required an explicit value for each enumeration's default.** With
field-level merging, "say nothing about travel" and "travel is billed as work
here" cannot both be the empty string: a project that set only its overtime
would otherwise silently cancel its customer's travel terms. So
`TravelBilling` gained `work` alongside `""`, and `ExpenseBilling` gained
`yes`. The empty string now means *inherit* everywhere.

**Terms are resolved for the day the entry belongs to, not for today.** An
entry recorded now for work done in March prices at March's agreement. Moving
an entry across a contract boundary re-prices it, which is correct: it is now a
different day's work. Already-recorded entries are untouched, because the
amount is frozen on the entry ([ADR-0014](0014-exact-money-and-duration.md));
dating the terms is what makes a *newly recorded* backdated entry right.

**The old columns are dropped, not deprecated.** The migration copies them into
one open-ended revision first. Keeping them as a second, unread copy is exactly
how the time-entry insert came to disagree with itself; two sources of truth
are worse than a migration.

## Consequences

**Positive**

* A renegotiation is a revision with a date and a reason, so an invoice from
  last year can be explained from the application.
* A project can differ from its account in one term without restating the rest.
* Backdated entries price correctly, which they previously did not.
* The terms screen can show the whole history and what it currently resolves
  to, which is the part people get wrong.

**Negative / accepted costs**

* **Two more indexed reads per billed entry**, where before the terms arrived
  with the customer row. Both are small and hit an index built for them, but
  billing an entry is now four reads rather than three.
* **Resolution is a rule people have to hold in their heads** — latest revision
  on or before the day, project over customer, field by field. The screen
  states the result to compensate, and the merge lives in one domain function
  so the billing path and the screen cannot disagree, but it is more to explain
  than "the customer has these terms".
* **A revision can be deleted**, which changes what future entries in that
  period are worth. Nothing points at a revision — entries carry the frozen
  rate, not a reference — so this cannot orphan history, but it can silently
  change what tomorrow's backdated entry costs. Deletion is audited.
* **No terms below the project.** An assignment cannot have its own; rates can.
  That asymmetry is deliberate — a contract is negotiated at the engagement
  level — but it is an asymmetry.
* Terms that changed mid-week make the overtime notice slightly arbitrary: it
  resolves for the week's start, so a threshold that moved on Wednesday is
  reported against Monday's agreement. Splitting the notice at the boundary
  would be more correct and much harder to read.

## Alternatives considered

**Keep the columns and add a history table** that is written on every change.
Rejected: the current terms would live in two places, and the "what applies on
this day" query would have to consult both.

**Whole-record override for projects.** Simpler to explain and to implement.
Rejected on the drift argument above — the copies would disagree after the
first renewal, and nobody would notice until an invoice did.

**Valid-from *and* valid-to on each revision.** Explicit rather than implied by
the next revision's start. Rejected: two dates can contradict each other (gaps,
overlaps) and there is no useful behaviour for a gap. A single start date makes
the timeline total by construction.

**Terms per assignment as well.** Rejected as more hierarchy than the problem
has; see the asymmetry above.

## Related

* [ADR-0024](0024-customer-rate-rules.md) — the rules themselves, and why the
  kind of time is chosen rather than derived. That decision stands; only where
  the terms live has changed.
* [ADR-0014](0014-exact-money-and-duration.md) — frozen amounts, which is why
  dating the terms affects new entries and not old ones.
