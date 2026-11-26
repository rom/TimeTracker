# ADR-0036: The weekend is a policy about arrival, not a lock on what is there

* **Status:** accepted
* **Date:** 2026-11-26
* **Addresses:** ASR-020

## Context

Most organisations using this application do not work weekends, and several have
asked for the Saturday and Sunday columns to be marked as such. A few want more
than a visual marker: they want the application to refuse time on those days, so
that a mistyped date is caught when it is typed rather than at invoice time.

The application already has a mechanism that refuses changes to time:
`checkPeriodOpen`, the week lock (ADR-0023). It is a single function that every
mutation of a time entry passes through, which is exactly what makes the lock
real — a new way to change an entry either calls it or the lock does not exist.

The obvious implementation is therefore to add the weekend test to that
function. It is the wrong one, and the reason is the difference between the two
questions being asked:

* The lock asks **"has this week been signed off?"** It is a statement about a
  week that has been through a process, and it covers every change to time in
  that week — creating, editing, moving, deleting, and stopping a timer that
  started inside it. That breadth is the point: a submitted week that could
  still be emptied by deleting its rows is not submitted.
* The weekend policy asks **"should this day be worked at all?"** It is a
  statement about a calendar, made once, applying to every week there has ever
  been and every week to come.

Fold the second into the first and the breadth of the lock is inherited by the
policy, with results nobody wants. The moment an administrator ticks the box:

* every entry already recorded on a Saturday — recorded before the setting
  existed, imported from a calendar, or entered during a genuine weekend
  callout — becomes impossible to correct, impossible to move to the right day,
  and impossible to delete;
* a timer started at ten on Friday evening and stopped at one on Saturday
  morning cannot be stopped, and runs for ever.

The second is the sharper case. Refusing a stop does not prevent the hour; it
prevents the hour from being *recorded correctly*, which is the one outcome
worse than an hour on a Saturday.

## Decision

**The weekend policy is a separate check, `checkWeekendAllowed`, called only
where new or moved time arrives.**

Three call sites, chosen because each is a point at which time lands on a date
it was not on before:

| Call site | Covers |
| --- | --- |
| `prepareEntry` | every way of creating an entry: the quick add, the correction form, a copied day, a routine, the CSV importer, the calendar importer |
| `StartTimer` | a timer beginning now, refused at the start rather than at the stop |
| `UpdateEntry` | an edit, and only when the edit moves the entry to a different day |

And, as deliberately, the places it is **not** called: stopping a timer,
deleting an entry, resolving observed idle time, confirming a proxy entry, and
restoring a backup. Every one of those is a way of getting existing time into a
correct state, and refusing it would strand the data the policy exists to
discourage.

The two checks sit next to each other in `periods.go` with a comment on each
saying why they are not one function, because the temptation to merge them will
recur.

The setting is instance-wide rather than per-user, like the week's first day: a
timesheet is read by somebody other than the person who filled it in, and an
approver looking at a week needs the same answer the person recording it had.
It defaults to permissive, because the column was added to databases with years
of weekends already in them.

## Consequences

**Positive**

* Turning the policy on is reversible in practice as well as in principle:
  nothing that was recordable before becomes unrecoverable after.
* A mistyped date is refused with a message naming the day it fell on, which is
  the useful half of the answer for somebody who believed they picked a working
  day.
* The lock keeps its one-function property. Nothing about ADR-0023 changes.

**Negative / accepted costs**

* Two similar-looking checks with different call sites is a thing a future
  reader can get wrong. Mitigated by a test that asserts the distinction
  directly — that `checkWeekendAllowed` refuses a Saturday while `DeleteEntry`
  on a Saturday entry still succeeds — so the merge fails loudly.
* An organisation that genuinely wants weekends frozen rather than merely
  discouraged does not get that here. They have the week lock, which is the
  mechanism for freezing time, and it already works per week.
* The weekend is Saturday and Sunday, not a configurable pair. See below.

## Alternatives considered

**Add the test to `checkPeriodOpen`.** Rejected above: it inherits a breadth
that strands existing rows and traps running timers.

**Refuse at the stop rather than the start of a timer.** Rejected. It refuses
after the work is done, with an hour recorded and nowhere to put it.

**Make the weekend days configurable.** Rejected for now. Which days are the
weekend has a different answer in a few parts of the world, but the application
has no other setting that varies by country, and a pair of day numbers in the
settings table would be a second week-shaped concept sitting beside `week_start`
without being connected to it. `domain.IsWeekend` is one function, so if the
need arrives it arrives as one change.

**Mark the days visually and refuse nothing.** This is the default, and for most
instances it is the whole feature. The refusal is the setting, not the baseline.

## Related

* [ADR-0023](0023-week-as-the-unit-of-approval.md) — the week lock, whose
  breadth this decision deliberately does not share.
* [ADR-0011](0011-theming-via-css-custom-properties.md) — why the weekend's
  colour is stored as a palette key rather than a value.
