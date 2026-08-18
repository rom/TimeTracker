# ADR-0023: The week is the unit of submission, approval and locking

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-020

## Context

Time that has been billed must stop moving. Until it is billed, it must be easy
to correct - and correcting it is the common case, not the exception: durations
get typed in the wrong unit, an afternoon lands on the wrong project, a timer is
left running overnight.

These two needs point in opposite directions, and a tool that picks one of them
fails at something real. Never locking means an invoiced figure can change
underneath the invoice. Always locking means the first typo is permanent, and
people work around it by inventing an adjustment somewhere else - which is worse
than the typo, because now the record disagrees with itself in a way nobody can
see.

So the question is not *whether* to lock but **what** to lock, **when**, and
**how to get back out**.

## Decision

**The unit of locking is one person's week, not the individual entry.**

A week has a status: `open`, `submitted`, `approved`, or `rejected`. A week with
no stored row is open, so the feature costs nothing for anyone who never uses it
and no migration has to invent history.

```
open ──submit──▶ submitted ──approve──▶ approved
  ▲                  │                      │
  └──withdraw────────┘                      │
  ◀──────────reject (with a reason)─────────┤
  ◀──────────reopen (deliberate)────────────┘
```

Four rules give the workflow its shape.

**Submitting locks the week to its owner.** That is what submitting *means*: it
says "these are my hours". Hours that keep changing afterwards are not a
declaration. `submitted` and `approved` both refuse changes; `rejected` does
not, because the owner has just been asked to correct it.

**Only a manager approves, and never their own week.** Approving your own
timesheet is not approval, whatever your role - an administrator working on a
project still cannot sign off their own hours. The single-user build refuses the
action outright rather than offering a control that means nothing.

**A rejection requires a reason.** "Rejected" alone leaves the owner guessing at
what to change, and they will resubmit the same hours. The reason is shown on
the screen where they will fix it, not only in an audit trail.

**An approved week can be reopened, deliberately and audibly.** It is a separate
operation with its own audit action and its own reason. A system with no way back
does not prevent corrections; it just moves them somewhere nobody is looking.

### One function enforces the lock

Every mutation of a time entry - create, update, delete, start a timer, stop a
timer, move entries between assignments - calls `checkPeriodOpen` before it
writes. That is the whole mechanism, deliberately: a new way to change an entry
either calls it or the lock does not exist for that path, and that is a
one-line, greppable property rather than a rule spread across a dozen handlers.

An **edit is checked at both ends**. Moving an entry out of an open week into a
submitted one changes the submitted week as surely as editing inside it, so the
existing week and the new week are both checked when a date changes.

**An administrator is not exempt.** A lock the most privileged user silently
walks through is not a lock. Reopening exists precisely so that unlocking is a
visible, recorded act rather than a side effect of who you are.

### Weeks are stored as a date, not a number

A period is keyed by the date its week starts on, in the owner's zone, resolved
through the instance's configurable week-start day. ISO week numbers were
rejected: they need a year alongside them to be meaningful, they disagree with
themselves across the new year, and they cannot express a Sunday-start week at
all.

### The submitted total is recorded

The week's total as it stood at submission is stored alongside the status, so an
approval is a decision about **specific figures** rather than about whatever the
week says at the moment it is opened. If the two ever differ - a restored backup
can do it - the approval queue flags the week rather than presenting the new
number as though it had been submitted.

## Consequences

**Positive**

* The question people actually ask - "can I still fix last week?" - has a simple
  answer, and the screen shows it before they start typing rather than after.
* An approved figure is stable, and every departure from that is recorded with
  who did it and why.
* The feature is invisible to anyone who does not use it: no submissions means
  every week is open and nothing behaves differently.

**Negative / accepted costs**

* **Every entry mutation costs two extra reads** - the settings row and the
  period row - both indexed and both single-row. Measured against writing an
  entry at all, this is noise; it is still work that a build with no approval
  workflow does not do.
* **A week is coarse.** Correcting one hour in an approved week means reopening
  the whole week, which unlocks the other thirty-nine hours while it is open.
  Locking individual entries would be finer, and is rejected below.
* **A timer running across a submission is refused at both ends.** Starting one
  inside a locked week fails, and a timer started before the lock cannot be
  stopped into it. The alternative - silently letting it land - would add hours
  to a week somebody has already signed off.
* Weeks that span a month boundary are submitted as weeks. An organisation that
  bills strictly by calendar month will submit a week whose hours fall in two
  invoicing periods.

## Alternatives considered

**Lock individual entries.** Finer, and it avoids reopening a whole week to fix
an hour. Rejected: it leaves a week half-frozen, so "is last week settled?" has
no answer, and the approval covers a set of entries that nobody can enumerate
afterwards.

**Lock by date cutoff** - everything before a date is frozen, set by an
administrator. Simple, and genuinely common. Rejected because it has no owner and
no workflow: nobody submits anything, nobody approves anything, and the audit
trail records a setting change rather than a decision about a person's hours.

**Approve by month.** Fewer decisions to make. Rejected: a month is too long to
wait before hours are checked, and a month's worth of corrections arrives as one
unreviewable block.

**Let an administrator bypass the lock.** Convenient, and every system that does
it ends up with the bypass as the normal path. Rejected in favour of reopen,
which does the same thing while leaving a record.

**Copy entries into an immutable snapshot at approval.** Genuinely stronger: the
approved figures could not change even if the entries did. Rejected as
disproportionate for now - it doubles the write path for every approval and needs
its own reporting story. The recorded submitted total is the cheap version of the
same guarantee, and the queue flags a mismatch rather than hiding it.

## Related

* [ADR-0004](0004-concurrent-timers.md) - overlapping entries; a locked week
  refuses new timers as well as new entries
* [ADR-0010](0010-audit-log-and-rsyslog.md) - submit, approve, reject and reopen
  are all audited actions
* [ADR-0015](0015-utc-storage-local-display.md) - which week an instant belongs to
  is decided in the owner's zone
* [ADR-0021](0021-json-backups-that-merge.md) - a restore can move a submitted
  week's total, which is why the mismatch flag exists
