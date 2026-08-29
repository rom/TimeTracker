# ADR-0033: Idle time is observed, never inferred

* **Status:** accepted
* **Date:** 2026-08-20
* **Addresses:** ASR-026

## Context

A timer left running through lunch is the second-commonest failure of any time
tracker, after one left running overnight. The overnight case is already handled:
a timer past `max_timer_seconds` is flagged and excluded from totals until
somebody resolves it, because eleven hours is obviously wrong. Lunch is not
obviously wrong. Six hours is a plausible morning, and nothing in the data says
which six hours it was.

DESIGN.md has promised idle detection with keep, discard and split since the
first draft. The difficulty is what "idle" can honestly mean here.

A desktop agent can see input devices and the focused application. This is a web
page, and a web page can see exactly two things about itself:

1. **Its own clock jumped.** The tick that was due a second ago arrived much
   later, so wall-clock time passed while nothing in the tab ran. Either the
   machine slept or the browser suspended the tab — and from inside the page
   those are the same event. Chrome freezes background tabs after about five
   minutes, so the two cannot be told apart even in principle.

2. **Nothing was typed or clicked in it.** The page ran and was visible
   throughout and saw no pointer, key or scroll event.

Neither of those is "the person was away". The second is weak enough to be
misleading on its own: a visible tab on a second monitor is untouched all day by
somebody working hard in an editor. The first is stronger — a sleeping machine
was not being worked on — but a suspended tab says nothing about the person at
all.

The tempting design is to call this idle detection, subtract the time, and be
done. That produces a tracker that removes hours somebody worked, on evidence
that does not support it, in a file they are about to invoice from. A missing
hour on an invoice is noticed when the total looks wrong; it is not noticed when
the total looks plausible.

## Decision

**Record the observation, not the conclusion. Change nothing without a person
saying so.**

Concretely:

* The stored row is an *observation* with a **source** — `asleep` or
  `untouched` — and no field anywhere claims the person was absent. The
  vocabulary is the same all the way from the SQL table to the sentence on
  screen: "this timer ran from 12:00 to 13:00 while the page was not running".
* **Three answers, and the default is keep.** Keep records the decision and
  changes nothing. Discard ends the entry where the stretch began. Split does
  that and records a second entry for what followed, so a break is removed
  without losing the work on the other side of it.
* **Every button carries its own arithmetic**, computed by the same function
  that applies it (`domain.ResolveIdle`), so the sentence on the button and the
  change it makes cannot drift apart. Discard means "and everything after it",
  which for a stretch in the middle of a six-hour entry is three hours, not one
  — so it says so, and asks again before doing it.
* **A resolution is an edit**, so it goes through the same week lock, the same
  authorisation and the same audit trail as editing the entry by hand. Removing
  an hour from an approved week by answering a prompt is the same change as
  removing it by hand.
* **Nobody else may file or answer one.** Not a manager, not an administrator.
  The strictness is deliberate and is not what the authorisation model would
  require on its own: "your tracker decided you were away" is a sentence this
  application should not be able to produce.
* **A running timer gets a notice, not a question.** Its interval is still being
  measured, so there is nothing stable to rewrite. Saying so while the timer runs
  is the point — it is the moment somebody can still remember what they were
  doing at half past twelve.
* **The browser's times are clamped**, to the entry and to the present. Not
  primarily as a security measure — an observation can only ever offer its own
  owner a choice — but because a clock a few minutes out is ordinary, and a
  stretch reaching outside its entry makes every figure downstream nonsense.
* **Reporting the same absence twice widens one row** rather than adding a
  second. A laptop woken and slept again inside one lunch hour reports exactly
  that, and being asked twice about one hour is how a prompt becomes something
  people click away without reading.

## Consequences

**Positive**

* The application never removes time on its own. Every hour that leaves a
  timesheet left because somebody pressed a button that told them what it would
  do.
* The weaker signal is still useful. `untouched` produces false positives by
  design, and "keep" is one click — the cost of a false positive is a click, and
  the cost of a false negative is a billed lunch.
* Because observations are stored rather than acted on, a resolution is a fact
  about the timesheet: "the application saw nothing here and I said it was work"
  survives in the row and in the audit trail.
* No new mechanism. The review sits beside gap detection and the long-timer
  flag, which are the same shape: observed, reported, never applied.

**Negative / accepted costs**

* **It cannot see the case it would most like to see.** Somebody who leaves the
  machine awake with the tab visible and goes to a meeting is not observed at
  all, and the application says nothing. That is the honest outcome, and it is
  why this is not called idle *detection* anywhere in the code.
* **`asleep` cannot distinguish a sleeping machine from a suspended tab**, so it
  can fire for somebody who was working in another window the whole time. The
  wording names both possibilities rather than picking the flattering one.
* One more table, one more index, and two queries on the Today screen.
* The threshold is instance-wide rather than per person. Fifteen minutes suits
  most people; somebody with a different rhythm is served by the switch rather
  than by their own number, which is one setting instead of a per-user
  preference nobody would find.

## Alternatives considered

**Subtract idle time automatically, and let the user add it back.** What most
trackers do. Rejected: the failure is silent and lands in an invoice. Adding time
back requires noticing it went, and the whole premise of the feature is that
somebody was not looking at the screen.

**Only report the strong signal (`asleep`).** Tempting, and it would remove every
false positive from `untouched`. Rejected because the strong signal is not
actually strong — a suspended tab is not a sleeping machine — so the honest
framing is needed either way, and once it is there the weaker signal costs a
click and catches the lunch on a desktop that never sleeps.

**Ask about it when the timer stops, as a modal.** A prompt in the way of the
thing somebody is trying to do gets dismissed rather than read, and this one can
remove three hours. It is a panel on the Today screen instead, which waits.

**A desktop agent that watches input devices.** The only way to know what the
machine was doing, and out of scope for the same reason it always was: it is a
second program to install, sign and update per platform, and DESIGN.md §9 already
records activity tracking as deferred behind an integration ADR.

## Related

* ADR-0002 — server-rendered with a small HTMX subset (why the watcher is 90
  lines of plain script and no library)
* ADR-0017 — no inline script at all, which is why the threshold reaches the page
  as a data attribute
* ADR-0023 — the week is the unit of approval, and a resolution respects its lock
