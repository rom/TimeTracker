# ADR-0015: Timestamps stored in UTC, displayed local

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-014, ASR-002

## Context

A time tracker is made of timestamps, and it will be used by people who travel, by
teams spread across time zones, and across daylight-saving transitions. Two facts
collide: an instant is absolute, but "which day did that work belong to" is local
and is what gets invoiced.

The transitions are where this bites. On the day a clock goes back, a local wall
time occurs twice; on the day it goes forward, some local times do not exist at all.
A timer running across either boundary must still report the right duration, and an
entry must land on the right invoice day.

## Decision

* **Every timestamp is stored in UTC**, as an ISO-8601 string with explicit offset
  in SQLite (which has no native timestamp type), so the stored value is
  unambiguous and sorts lexicographically.
* **Durations are computed from the UTC instants**, so a timer spanning a DST
  transition reports the real elapsed time rather than the wall-clock difference.
* **The owning day of an entry is its start instant projected into the entry's
  time zone**, and that **IANA time zone name is stored on the entry** — not merely
  an offset, and not inferred at report time from the reader's current location.
  Otherwise a consultant who works in Stockholm and reports from New York would see
  their Monday evening move to Sunday, and the invoice would change.
* Reports take an explicit time zone (defaulting to the user's), so the same range
  produces the same answer for everyone looking at it.
* Go's `time` package and IANA database are used throughout. The binary embeds the
  time zone database (`import _ "time/tzdata"`), because a stock Windows machine has
  no `/usr/share/zoneinfo` — ASR-002 would otherwise fail in a way that only appears
  on one platform.
* Week boundaries and the first day of the week are configurable (ISO 8601 Monday
  default), since weekly timesheets are a billing artefact.

## Consequences

**Positive**

* Correct durations across DST and travel, and stable day attribution regardless of
  where a report is run.
* Unambiguous storage that any external tool can read.
* Identical behaviour on all three platforms, including Windows without a system
  zoneinfo.

**Negative / accepted costs**

* Every display path must convert explicitly; a raw stored value shown to a user is
  a bug, and one that only looks wrong to users outside UTC.
* The embedded tzdata adds a few hundred kilobytes and ages with the binary — a
  political change to a time zone requires a rebuild.
* Storing a zone per entry is an extra column and an extra decision when creating
  entries in bulk (we inherit the user's current zone).
* Two entries at the "same" local time on a fall-back day are genuinely one hour
  apart, which is correct but reads oddly on screen; the UI shows the offset when
  it is ambiguous.

## Alternatives considered

**Store local wall time with a separate offset column** — reports need no
conversion. Rejected: durations across DST become wrong, and comparing instants
requires reconstructing UTC anyway.

**Store Unix epoch integers** — compact and fast. Rejected for readability of the
raw database (ASR-011) and for losing the sub-second-free ISO form that external
tools read directly; the sort order and comparison advantages are equal.

**Everything in UTC including display** — no ambiguity ever. Rejected as
user-hostile: nobody wants to see their Tuesday afternoon labelled 13:00Z.

## Related

* ADR-0014 (exact durations), ADR-0004 (concurrent timers)
