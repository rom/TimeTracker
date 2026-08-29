# ADR-0034: Reminders are shown, not sent

* **Status:** accepted
* **Date:** 2026-08-29
* **Addresses:** ASR-027

## Context

DESIGN.md §9 has promised end-of-day and end-of-week nudges since the first
draft: a timer left running, a day with nothing on it, time a colleague recorded
for you, a week nobody submitted. Each of those is a real cost — the running
timer is hours, the unsubmitted week is a delayed invoice — and each is invisible
precisely because the person is not looking.

"Reminder" normally means a message. That reading points at machinery this
application does not have and would be poorly placed to acquire:

* **A scheduler.** Something has to notice that it is five o'clock. This is a
  single binary with no daemon and no background job, which is what makes it
  possible to run it locally by double-clicking it (ADR-0001).
* **A channel.** A message has to go somewhere. Mail means SMTP configuration,
  deliverability, a from-address, and somebody's password in a config file; push
  means a service worker, a subscription store and a third-party endpoint. There
  is no mail in this tree at all today.
* **A queue.** A message that was sent is a fact independent of the timesheet. It
  can arrive about a timer that was stopped ten minutes ago, or about a week
  submitted while it sat in a queue. Keeping it in step means invalidating it,
  which means storing it and reasoning about it.

That last one is the substantive objection rather than the practical ones. A sent
reminder is a claim about the past. What is wanted is a claim about the present.

## Decision

**A reminder is computed when a screen renders, from the timesheet as it stands.
Nothing is queued, sent or delivered.**

Four of them, all questions asked of state that already exists:

| Nudge | True when |
|---|---|
| a timer is still running | any timer is going, past the reminder hour |
| nothing recorded today | the day has no entries, past the reminder hour |
| a colleague recorded time for you | the proxy inbox is not empty |
| the week is not submitted | the week has time, is open, and is ending |

Consequences of computing rather than sending:

* **A nudge cannot be stale.** Recording the time makes it untrue, and untrue
  nudges are not computed. There is nothing to invalidate and nothing to clean
  up.
* **The only thing stored is a dismissal** — "I know" is the one part of this the
  timesheet cannot answer on its own. It is scoped to the day or the week, so
  tomorrow asks again. A nudge that cannot be waved away is nagging; one waved
  away for good is a feature quietly switched off.
* **The window is the person's local afternoon, not the server's.** An instance
  serving Stockholm and Chicago nudges each of them at the end of their own day.
* **The week nudge waits for the last working day**, which for a Monday-start
  week is Friday afternoon, and follows the configured week start rather than
  naming Friday. Sunday evening was the first rule and is the wrong one: nobody
  is there to read it, so it gets cleared unread on Monday. Wednesday is worse —
  every week is unsubmitted on Wednesday, and a panel that is always there is one
  people stop seeing.
* **An empty week is not nudged about.** Somebody who recorded nothing all week
  was ill, on holiday or between clients, and telling them their empty week is
  unsubmitted is the application talking about itself.
* **The nudges appear on the day and week screens only**, and only when those
  screens are showing the current day or week. A prompt about today, rendered
  under a day three weeks ago, is a puzzle.

## Consequences

**Positive**

* No scheduler, no mail, no queue, no third-party endpoint, and no new
  dependency. The feature is two settings columns, one table of dismissals and a
  handful of counting queries.
* Nothing can arrive about something already dealt with, which is the failure
  that makes people ignore notifications generally.
* It works identically in local and server mode, which a mail-based design could
  not: local mode has no addresses and no way to send anything.

**Negative / accepted costs**

* **It only reaches somebody who opens the application.** That is the real limit,
  and it is not a small one: the person who most needs the end-of-day nudge is
  the one who has already closed the laptop. What this does catch is the next
  time they look, which is early enough to fix a Tuesday and much earlier than
  the end of the month.
* Four counting queries on the day and week screens. They land on the day view's
  ASR-012 budget, which is why every one of them counts rather than builds.
* The threshold hour is instance-wide rather than per person, like the idle
  threshold. One setting an administrator understands beats a preference nobody
  finds.
* Dismissals accumulate one row per person per nudge per day. Small, and never
  read after their day passes — but they are not swept, so a long-lived instance
  keeps them. Worth a sweep if that ever shows up in a backup's size; it has not.

## Alternatives considered

**Send mail at five o'clock.** The obvious reading, and the one that would reach
somebody who has closed the laptop. Rejected for the pile of machinery above, and
because the first version would immediately face the stale-message problem: mail
sent at 17:00 about a timer stopped at 17:01 is worse than no mail, since it
teaches people that the reminders are wrong.

**A browser notification from the running page.** No server work, and it can fire
while the tab is open. Rejected because it needs a permission prompt on first
load — the interruption people have learned to refuse — and it only fires while
the page is open, which is the case the panel already covers.

**Nudge whenever the condition is true, with no hour and no window.** Simpler, and
it makes the panel permanent furniture: every week is unsubmitted on Wednesday,
and a panel that is always there is one nobody reads. The window exists so that
seeing the panel means something.

**Store a reminder row and mark it read.** The shape most notification systems
have. It buys nothing here — every condition is derivable from the timesheet in a
counting query — and costs the invalidation problem in full.

## Related

* ADR-0001 — single binary, two modes: why there is no daemon to schedule this
* ADR-0027 — routines are offered, never fired: the same refusal to act on the
  passage of time, one layer up
* ADR-0033 — idle time is observed, never inferred: the neighbouring panel, and
  the same rule that a prompt is not an action
