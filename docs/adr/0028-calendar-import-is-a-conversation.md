# ADR-0028: A calendar is not a timesheet, so importing one is a conversation

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-024

## Context

"Import my calendar" sounds like a data-format problem. It is not; the format is
the easy half. RFC 5545 is well specified, Outlook, Google and Apple all emit
it, and the parts that break naive parsers — line folding at 75 octets, escaped
separators, the three DTSTART forms — are finite and testable.

The hard half is that **a calendar is not a record of work.** It contains:

* meetings that were cancelled;
* meetings the person declined and did not attend;
* all-day entries marking public holidays and other people's leave;
* blocks somebody put in to protect an afternoon from being booked;
* recurring series, of which the export contains one instance;
* meetings that overran, finished early, or were replaced by a corridor
  conversation.

Importing a calendar wholesale produces a week that looks entirely plausible and
is wrong. That combination is the problem: a missing hour gets noticed because a
total looks low, but an extra hour that looks like the forty around it does not.

## Decision

**Two passes, and every event accounted for by name.**

The first pass parses the file, matches each event against the catalogue, and
writes nothing — the same shape as the CSV import
([ADR-0022](0022-two-pass-csv-import.md)). The second imports only what the
person ticked.

What separates this from the CSV import is that **the preview lists events it
will not import, and says why in words.** Cancelled, declined, all-day,
recurring, no length, no assignment matched — each is shown against the meeting
it concerns. A count of "34 of 41 importable" is not enough: somebody has to be
able to see that the missing hour on Tuesday was a meeting they declined, and
agree with that.

**Matching refuses to guess.** An event's words are matched against the
catalogue in two tiers: forms that name an assignment first, then forms that
name only a project. The first tier producing exactly one candidate wins. A tie
matches nothing and the person picks from a dropdown, because "Acme migration"
naming a project with two assignments is genuinely ambiguous, and guessing which
one is guessing whose invoice the hour lands on.

**Recurrence rules are not expanded.** An RRULE describes an infinite series;
expanding it correctly needs the exception dates and the zone the rule is
anchored in. The first instance is imported if asked for, and the event is
labelled as repeating so nobody thinks the other fifty came too.

**Re-importing is safe.** People export again next month and the ranges overlap.
An event already imported — same assignment, same start, same length — is
flagged and not pre-selected.

**No link is stored back to the calendar.** The imported entry is time somebody
worked, not a reference to a meeting. Storing the calendar's UID on it would
invite the calendar to own the timesheet: the next question would be what
happens when the meeting moves, and the answer would have to be that an external
system edits recorded hours.

**Import is per-event, not all-or-nothing.** This differs from the CSV import
deliberately, because the unit differs. A CSV row is a line of a document
somebody is importing whole; a calendar event is one meeting they individually
ticked. Refusing all forty because the thirty-first falls in a submitted week
would be unhelpful, so each failure is reported against its meeting.

## Consequences

**Positive**

* Hours that reach the timesheet are hours a person agreed were work.
* The events that are *not* imported are visible, which is what makes the
  result checkable against the calendar it came from.
* An overlapping re-import is a normal, safe operation.

**Negative / accepted costs**

* **It is a lot of clicking** for a busy calendar. Somebody importing a month of
  meetings reviews every one. The default-assignment field and the pre-selection
  reduce it; they do not remove it, and that is the intended trade.
* **The file is uploaded once and carried back through the form** so the commit
  step does not need it again. It is in the page, which is a size limit on
  practical imports; a calendar export is small enough that this is a better
  trade than asking somebody to find the file again after reviewing forty rows.
* **The duplicate check is heuristic.** It matches on the entry rather than on a
  stored identifier, so a meeting imported and then edited will import again.
* **No subscription.** There is no polling of a calendar URL, so this is a
  manual operation each time. That is consistent with everything else here being
  explicit, but it is less convenient than a sync.
* Only the parts of RFC 5545 a timesheet needs are read. Attendee lists beyond
  the declined check, alarms, free/busy and journals are ignored.

## Alternatives considered

**Import everything and let the user delete what is wrong.** Fewer clicks up
front. Rejected: deleting requires noticing, and the whole failure mode here is
that a wrong week looks right.

**Subscribe to a calendar URL and sync continuously.** Genuinely more
convenient. Rejected for now on the same ground as the scheduler in
[ADR-0027](0027-routines-are-offered-not-fired.md): a background process that
creates billable time without anybody looking is the thing this application is
consistently unwilling to build.

**Match events to assignments with fuzzy scoring** rather than exact word
containment, and take the best score. Rejected: a confident wrong match is worse
than no match, and a score threshold is a knob nobody can tune from the outside.

## Related

* [ADR-0022](0022-two-pass-csv-import.md) — the preview-then-commit shape this
  follows, and where it deliberately differs
* [ADR-0027](0027-routines-are-offered-not-fired.md) — the same refusal to
  create time nobody asked for
