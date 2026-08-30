# ADR-0035: A burn rate is a projection, and says so

* **Status:** accepted
* **Date:** 2026-08-30
* **Addresses:** ASR-028

## Context

`projects` has carried `budget_seconds` and `budget_minor` since the first
migration, and DESIGN.md §6 has listed budget consumption as part of reporting
for as long. Neither was reachable: no form set them and no screen read them.
Layer 5 recorded the gap and moved on.

The consumption half is straightforward — sum what has been recorded against the
project and subtract. The half that needs deciding is the one people actually
want, which is *when does this run out*.

That is a forecast, and a forecast in a billing tool is a peculiar object. It
looks exactly like the other numbers on the screen — same table, same alignment,
same tabular figures as the hours that are facts — while being a guess that
depends entirely on the coming weeks resembling the recent ones. Somebody reads
"runs out 22 November", tells a client the engagement is funded to late
November, and has now made a commitment out of an average of four weeks of
timesheets.

There is also a real temptation to make the forecast cleverer: weight recent
weeks more heavily, fit a trend, exclude outliers. Every one of those makes the
number less checkable, and a projection nobody can check in their head is one
nobody can sanity-test before repeating it to a client.

## Decision

**Report consumption as arithmetic. Report burn as an average over a stated
window, refuse to project when the evidence is thin, and never render an
estimate as though it were a measurement.**

Concretely:

* **The rate is the mean over the last four weeks' *active* weeks.** Four is
  long enough that one quiet week does not halve the estimate and short enough
  to notice a project that has just picked up. Averaging over the whole life of
  a project reports a rate nobody recognises: two hours a week for a year and
  forty this month is not "three hours a week".
* **Averaged over the weeks that had work, not over the window.** A fortnight's
  holiday inside the window would otherwise halve the rate and double the
  runway. Dividing by active weeks makes the estimate the more pessimistic of
  the two readings, which is the right direction to be wrong in when the number
  is used to decide whether to ask for more budget.
* **Four refusals, each carrying its reason**: no budget, already over, nothing
  recorded recently, too little history (fewer than two active weeks). The
  reason is shown in the cell. A blank cell in a "runs out" column reads as
  *never*, which is the opposite of "we cannot tell".
* **Both caps, and whichever binds.** A project can have an hours budget, a
  money budget or both, and it stops at the first one it reaches — so the
  headline percentage is the worse of the two and the projected date is the
  earlier.
* **An overrun is shown, not clamped.** The percentage goes past 100 and the
  remaining figure goes negative, because "twelve hours over" is the number
  somebody needs. A row clamped at 100% looks exactly like a project that landed
  precisely on budget. Only the *bar* is capped, because a bar 340% wide is a
  layout bug rather than emphasis.
* **The window is stated on the screen**, in the sentence above the table, along
  with the fact that these are estimates rather than commitments.
* **Consumption is the same "counting" rule as every other total**: confirmed
  and not flagged. A proposal nobody has accepted and an entry marked for review
  are questions, not consumption, and counting them would report a project over
  its cap because somebody mistyped an hour.
* **Hours are hours, money is billed money.** The hours figure counts
  non-billable work too — an hour against an hours budget is an hour somebody
  worked, whoever pays for it — while the money figure is the billed amount, so
  only billable work moves it.

## Consequences

**Positive**

* The arithmetic is checkable. A reader can multiply the weekly rate by the
  weeks left and get the remaining budget, which is the property that makes a
  forecast safe to repeat to somebody else.
* The refusals are the useful part. "Too little history to estimate" on a
  project that started last week is worth more than a confident date derived
  from one data point.
* Three queries regardless of how many projects there are, so the screen costs
  the same on a consultancy as on a demo.
* Two fields that have been dead in the schema since the first migration are now
  reachable, and a report that was documented but absent exists.

**Negative / accepted costs**

* **The estimate is bad exactly when a project changes shape**, which is when
  somebody is most likely to look at it. A project ramping up reads as having
  more runway than it has. The pessimistic averaging helps a little and does not
  fix it; the window being stated is what lets a reader discount it.
* Four weeks is a fixed window rather than a setting. One more knob on the
  settings screen buys a number most people would leave alone, and the honest
  answer to "why four" is that it is a compromise no configuration makes less
  arbitrary.
* Active weeks are counted with `strftime('%Y-%W')`, which does not follow the
  configured week start. Deliberate: a rate averaged over "four weeks" should not
  become a different number because an instance starts its weeks on Sunday, and
  the count is a denominator rather than a date.
* The report is gated on the permission that governs money, so a member sees
  nothing at all rather than an hours-only view. That is a real loss for
  somebody running a project without commercial access, and the alternative — a
  budget report with the budgets removed — is an empty table with a heading,
  which tells them there is something they are not being shown.

## Alternatives considered

**Project from the whole life of the project.** More data, and worse: it
describes a project's history rather than its present, and the present is what
"runs out" is about.

**A weighted or fitted trend.** Better on paper for a project with a clean ramp,
and unusable for the thing this is for: nobody can check it, and a forecast that
cannot be checked cannot safely be repeated to a client.

**Warn at a threshold — 80% consumed, say.** Deferred rather than rejected. The
report is sorted worst-first, which surfaces the same projects without needing a
line drawn at a number that would be wrong for somebody. A nudge on the day
screen (ADR-0034) is the natural home if it earns its place later.

**Put a budget column on the project list in Admin.** The catalogue is where a
project is set up; this is asked at a different time by a different person, and
the question needs a rate, a projection and a sort order that the catalogue has
no business carrying.

## Related

* ADR-0014 — exact money and duration: why a budget is minor units and seconds
* ADR-0027 / ADR-0033 / ADR-0034 — the same rule from three other angles: the
  application reports what it observes and leaves the judgement to a person
* ADR-0032 — the query shape the consumption totals are written to
