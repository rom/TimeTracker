# ADR-0005: Proxy entries require the subject's confirmation

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-008, ASR-006

## Context

A frequent, genuine need: two people work together, one of them tracks time
diligently and the other does not. At the end of the week the diligent one knows
perfectly well that the colleague spent Tuesday afternoon on the same assignment,
but that time is unrecorded and therefore unbilled.

Letting one user write directly into another's timesheet solves the practical
problem and creates a serious one. Hours recorded in someone's name become the basis
of invoices, payroll, and utilisation assessments. In most jurisdictions, records of
another person's working time that they never saw are a labour-law and works-council
problem, and in every jurisdiction they are a trust problem.

## Decision

A user with the appropriate permission may create a time entry **on behalf of**
another user. Such an entry:

* stores `user_id` = the subject (whose time it is) and `entered_by` = the author,
  which are always both persisted, never collapsed;
* is created with status **`pending`**;
* is **excluded from every total, report, export and invoice** for the subject while
  pending;
* appears in the subject's inbox, where they **accept** (status becomes `confirmed`
  and it counts), **edit then accept** (the edit is recorded), or **reject** (status
  becomes `rejected`, retained with the reason, never deleted);
* generates audit events for creation and for the subject's decision (ADR-0010).

The subject can always see who entered time in their name. A user cannot proxy for
themselves to bypass anything, and rejected entries are never resurrected by the
author — a new proposal is a new entry.

Permission to propose is separate from permission to approve timesheets: a `member`
may propose for a colleague on a shared project; a `manager` may propose for their
team; nobody may confirm on someone else's behalf.

## Consequences

**Positive**

* The practical problem is solved — the colleague's hours get captured while the
  detail is fresh.
* Consent is structural, not procedural: there is no code path that adds unconfirmed
  time to a total, so it cannot be bypassed by a UI shortcut or an API client.
* Disputes are answerable from the record: who claimed it, who accepted it, when.

**Negative / accepted costs**

* Time proposed for a colleague who never checks their inbox is never billed. This
  is intentional — the alternative is billing for hours nobody confirmed — but it
  needs a nudge: pending items are surfaced prominently and included in reminders.
* Extra state on the entry (`status`, `entered_by`, decision timestamps) that
  complicates every query touching totals. Mitigated by making the "billable,
  confirmed" filter a single shared query helper rather than a condition repeated at
  each call site.
* A manager and a colleague can both propose for the same period, producing
  near-duplicate pending entries. The inbox flags overlapping proposals.

## Alternatives considered

**Direct write with notification** — fastest to use, and defensible inside a small
trusted team. Rejected on ASR-008: it produces records of someone's hours that they
never agreed to, which is precisely the thing that becomes a dispute.

**Track it on the author's own timesheet, tagged with the colleague** — no
cross-user writes at all, trivially safe. Rejected: the colleague's timesheet stays
empty, so utilisation and payroll are still wrong, and the author's hours are
inflated.

**Shared team entry fanned out per participant** — good for meetings specifically.
Not rejected outright: it is a candidate feature layered *on top* of this decision,
where the fan-out creates one pending proposal per participant.

## Related

* ADR-0008 (RBAC), ADR-0010 (audit log)
