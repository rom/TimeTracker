# ADR-0027: Recurring entries are offered, never created on a schedule

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-023

## Context

Most recorded time is not novel. Lunch, the daily stand-up, the Monday project
meeting, Friday administration: the same handful of entries, the same lengths,
week after week. Typing them again every day is exactly the tedium that makes
people stop filling in a timesheet as they go and reconstruct the week on Friday
from memory — which is where under-reporting comes from.

So the feature is obviously worth having. The question is what "recurring
entry" means, and there are two candidate answers.

**A scheduler.** The routine has a rule, and the application creates the entries
when the rule fires. This is what a calendar does, and it is what most people
picture.

**A template.** The routine describes an entry; the day view offers what is due;
a person clicks it.

## Decision

**A template. Nothing is created until somebody asks for it, on the day.**

The day view shows the routines that apply to that weekday. Each is one click.
A routine that already looks recorded — same assignment, same length, same day —
is shown ticked and disabled rather than hidden, so the row does not change
shape as the day fills in and nobody has to wonder whether they already did it.
"Add all" applies the outstanding ones together.

The reason is that **a scheduler would invent billable hours.** An entry created
because the calendar said Tuesday is an hour nobody worked, and it is created
whether the person was ill, on holiday, in a different country, or had simply
left the company. It would reach a timesheet, then an approval, then an invoice,
and the first person with any reason to question it would be the client.

The failure is also silent in the worst way: it produces a *plausible* week. A
missing hour gets noticed because a total looks wrong. An extra hour that looks
exactly like the forty before it does not.

Supporting decisions:

* **A routine belongs to one person.** It is a way of typing, not a shared
  object. Applying somebody else's would record their habits as your time.
* **Weekdays, not a general recurrence rule.** "Every other Tuesday except in
  August" is a real thing calendars express, and expressing it here would mean
  implementing RRULE — and then answering what happens to an exception when the
  underlying entry was already recorded. Weekdays cover lunch, stand-ups and
  admin, which is what people actually asked for.
* **The entries a routine produces are ordinary entries.** They carry no
  reference back to it. Deleting a routine changes nothing that was recorded,
  and editing one does not rewrite history.
* **The "already recorded" check is a guess, deliberately.** It matches on
  assignment and length rather than on a stored link. A stored link would be
  exact and would make the entry a routine's property; the guess is occasionally
  wrong in the harmless direction — it will suppress a genuine second stand-up
  of the same length on the same day — and keeps the entry independent.

## Consequences

**Positive**

* The application never records time nobody worked.
* Recording a routine day is one click, or one click for all of them.
* A routine can be paused rather than deleted, for the thing that happens every
  week until the project it belongs to pauses.

**Negative / accepted costs**

* **It still needs a click.** Somebody who never opens the day view on Tuesday
  has no stand-up recorded for Tuesday. That is the point, and it is a real cost
  against the scheduler that would have filled it in.
* **The duplicate check can suppress a legitimate entry**: two thirty-minute
  meetings on the same assignment on the same day, and the second looks like the
  first. The manual entry form is unaffected, so the workaround is the ordinary
  path, but the routine button will look done when it is not.
* **No general recurrence.** Fortnightly, monthly, and "first Monday" are not
  expressible.
* A routine that names an assignment which is later archived stays in the list
  and fails when applied. Validating on save would not help — the archiving
  happens afterwards.

## Alternatives considered

**A scheduler with a confirmation step** — create the entries but mark them
pending until confirmed. Rejected: the application already has a pending state
with a specific meaning (proxy proposals, ADR-0005), and overloading it with
"time the computer guessed" would make the inbox two things at once. It also
does not solve the problem, only relocate it: an unreviewed pending queue
becomes an approval nobody reads.

**A scheduler that only fills in days with other time on them** — the person
clearly worked, so the stand-up presumably happened. Rejected: "presumably" is
doing a lot of work in a sentence about an invoice.

**Full RRULE support**, as the calendar import deliberately avoids. Rejected for
the same reason it is avoided there: correct expansion needs exception dates and
an anchor zone, and getting it subtly wrong invents entries that were never
agreed.

## Related

* [ADR-0005](0005-proxy-time-entry.md) — the other place this application
  refuses to record time somebody did not confirm
* [ADR-0028](0028-calendar-import-is-a-conversation.md) — the same judgement
  applied to an external calendar
