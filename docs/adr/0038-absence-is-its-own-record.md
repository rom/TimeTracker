# ADR-0038: Absence is its own record, not a kind of time

* **Status:** accepted
* **Date:** 2026-11-27
* **Addresses:** ASR-020, ASR-027

## Context

The application could not record a day off. Entry kinds are work, overtime and
travel; there was no holiday, no sick day, and no public holiday.

That is worse than a missing feature, because the application *notices* the
resulting gap and complains about it. A week on leave is a week of empty days,
and empty days are what the end-of-week reminder exists to report (ADR-0034).
The day pane's unaccounted-time banner says the same thing about each
individual day. So a timesheet that cannot record leave does not merely fail to
record it — it tells somebody off for taking it.

The weekend work made the hole plain. The instance now knows that Saturdays are
not normally worked, can refuse time on them, and shades them in both grids. It
has never known anything about the 25th of December.

Without somewhere to put leave, people do one of two things: leave the week
empty and ignore the nagging, or create a customer called "Internal" with a
project called "Absence" to book it against. The second is the common one, and
it puts leave into the catalogue, into the pickers, into the reports grouped by
customer, and into anything that counts customers.

## Decision

**An absence is its own record: a person, a kind, and a span of days.**

```
absences(id, user_id, kind, starts_on, ends_on, note, created_by, created_at)
```

Five kinds: holiday, sick, public, parental, other.

### Not a kind of time entry

The obvious alternative, and much less code: an entry already has a kind, and
reusing it would inherit creation, editing, approval, locking and the recycle
bin for free.

Rejected because an entry needs an assignment, which needs a project, which
needs a customer. Recording a holiday as time therefore requires a fake
customer in every instance, and leave then appears in every report that groups
by customer — which is the thing people are working around today, enshrined.

An absence is about a person and a date and nothing else, so it is stored as a
person and a date and nothing else.

### Dates, not instants

`starts_on` and `ends_on` are `YYYY-MM-DD`, inclusive at both ends. A day off is
a day in the calendar somebody lives in, not an interval on a clock: it does not
begin at midnight UTC, and storing it as a timestamp would make a holiday in
Auckland start on the wrong day. The overlap test and the "does this cover that
day" test are both string comparisons, which sort the way the dates do.

### A public holiday belongs to the instance

`user_id` of 0, the same convention the recycle bin uses. It applies to
everybody, so recording one is administration (ADR-0008), and it counts towards
every person's week without any of them doing anything.

### Absence fills its share of the expected week

A day away is worth the expected week divided by its *working* days, so one day
of a Monday-to-Friday week is worth a fifth and a week off is worth all of it.
A week entirely on leave reads as complete rather than as thirty-eight hours
short. A weekend day booked as leave is worth nothing, because the share is of
the working week.

### It is not subject to the week lock

Deliberately. An absence adds no hours and changes no approved figure; it
records that somebody was away. Refusing last month's sick day because the week
has been signed off would leave the record permanently wrong, which is the
opposite of what the lock is for.

### It is bounded by its owner, not by a project

Your own absence is yours. Anybody else's belongs to whoever administers the
instance. A manager is deliberately not given it: a manager's authority is
scoped by project membership, and an absence names no project to scope them
by.

This was got wrong first. The initial version borrowed the proxy rule used for
recording time on a colleague's behalf, which binds a member to the projects
they share with the subject — so the resource reached `requireMembership`
carrying no project, no customer and no id, fell through its last clause, and
was allowed. Any member could delete any colleague's holiday. It is exactly the
shape of the defects fixed when the client role was built, and a test written
alongside the feature caught it.

## Consequences

**Positive**

* A week off is a complete week, and the reminders leave it alone.
* No fake customer, so the catalogue, the pickers and the reports stay about
  work.
* Public holidays are recorded once for everybody.
* Deleting one goes through the recycle bin like any other record
  (ADR-0037).

**Negative / accepted costs**

* A second thing that occupies a day, which the week and month grids must now
  both draw. The alternative put it in the catalogue, where it would have been
  drawn wrongly by more things.
* Absence has no approval workflow. It is recorded, not requested: this is a
  timesheet, not a leave-management system, and an approval queue for holidays
  is a different product.
* A manager cannot record absence for their team, only an administrator can.
  If that proves wrong, the fix is a deliberate rule about who may act for
  whom, not a resource borrowed from somewhere else.
* Entitlement — how many days somebody has left — is not modelled at all.

## Alternatives considered

**A fourth entry kind.** Rejected above: the fake customer is the whole
problem.

**Public holidays only, with no personal leave.** Smaller, and solves the 25th
of December without touching anybody's timesheet. Rejected because the nagging
is mostly about personal leave, which is the common case.

**An approval workflow for leave.** Out of scope; see the costs above.

## Related

* [ADR-0034](0034-reminders-are-shown-not-sent.md) — the reminders that were
  nagging about leave.
* [ADR-0037](0037-deletion-is-recoverable.md) — how an absence is deleted.
* [ADR-0008](0008-rbac-model.md) — why a public holiday is the
  administrator's.
