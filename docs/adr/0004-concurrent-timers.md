# ADR-0004: Multiple concurrent timers, overlaps allowed

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-001, ASR-014

## Context

A consultant's day is not a sequence of disjoint intervals. A build runs for
twenty minutes against one client while a meeting for another client is in progress;
a support incident interrupts feature work without ending it. Most trackers model
"the current task" as a single value, which forces the user to lie by omission —
whichever task they forgot to switch back to loses its time.

Allowing overlap creates its own problem: the sum of entries for a day can exceed
the elapsed hours of that day, which looks like an error to anyone reading a report,
and can look like over-billing to a client.

## Decision

A user may have **any number of running timers simultaneously**. A time entry is an
interval `[started_at, ended_at)` with `ended_at = NULL` while running; there is no
uniqueness constraint on "running entry per user".

Overlap is legal, and never silently altered. Instead the system makes it visible:

* the day view draws overlapping entries side by side, not stacked;
* daily and weekly totals report both **summed time** (the sum of entry durations)
  and **elapsed coverage** (the union of the intervals), so a 10-hour sum over an
  8-hour footprint is immediately legible;
* an entry that overlaps another is marked in the UI, as information, not an error.

Deliberately excluded: automatic splitting of overlapping time between assignments.
Dividing a 30-minute overlap into 15 minutes each is a guess, and a guess that ends
up on an invoice. If a user wants that, they will do it explicitly.

Timers are bounded by two safety rules, because "forgot to stop it" is the dominant
failure mode: a timer running past a configurable maximum (default 12 hours) is
flagged for review rather than counted silently, and idle detection (ADR pending,
see MVP_PLAN) offers to trim or split a timer after a period of inactivity.

## Consequences

**Positive**

* The model matches how the work actually happens, so users are not forced to
  falsify their day to fit the tool.
* No data loss from switching tasks and forgetting to switch back.
* Reports can state honestly both what was worked and what window it spanned.

**Negative / accepted costs**

* Every reporting query must be explicit about whether it sums durations or unions
  intervals. Two totals must be explained in the UI, which is a documentation cost.
* Utilisation and "how full was my day" metrics become ambiguous and need a defined
  convention (we use elapsed coverage for utilisation, summed time for billing).
* Users can accidentally leave several timers running, so the UI must make running
  timers impossible to miss — a persistent header showing every running timer with a
  live clock.

## Alternatives considered

**One running timer, auto-stopping the previous** — cleaner data, totals always
reconcile, simpler UI. Rejected on ASR-001: it structurally cannot represent genuine
parallel work, and its failure mode is silent under-billing.

**Allow overlap but auto-split time proportionally** — makes totals reconcile.
Rejected because it fabricates numbers that are then billed to clients.

**Reject overlapping entries with a validation error** — rejected: the user is
describing reality, and the tool's job is to record it, not to argue.

## Related

* ADR-0014 (exact durations), ADR-0015 (UTC storage)
