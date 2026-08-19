# ADR-0032: Index for the query that is written, not the one that is meant

* **Status:** accepted
* **Date:** 2026-08-19
* **Addresses:** ASR-012

## Context

`make test-perf` ran `go test -tags perf -run TestPerf`. No such test existed,
so it passed in silence, and TEST.md went on claiming ASR-012 was proved by it.
Writing the suite took an afternoon. What it found took rather longer to fix.

Against the 100,000-entry dataset the requirement names, on the machine this was
written on:

| Operation | Budget | Measured |
|---|---|---|
| day view | 100 ms | **365 ms** |
| week view | 100 ms | **350 ms** |
| timer start/stop | 100 ms | **303 ms** |
| one-year report | 2 s | 31 ms |

The last row is the interesting one. A report covering a year cannot be ten
times faster than a screen covering a day unless the day screen is doing
something unrelated to the day — and it was. Three queries were walking every
entry the user had.

None of them lacked an index. Each had one, and each failed to use it, for a
different reason:

**A partial index the planner declined.** `idx_entries_running` covers exactly a
user's unfinished entries. The planner preferred the broader
`idx_entries_user_started` and walked the lot — 95 ms for a query whose answer is
almost always empty, on every page render.

**A predicate that did not match.** The inbox badge asks for
`status IN ('pending')`. The index is partial on `status != 'confirmed'`. The
first implies the second to a reader; SQLite's partial-index matcher does not
make the inference, so it fell back to a scan.

**A function around the column.** The week banner summed a week with
`date(started_at) >= date(?)`. Wrapping the column makes the condition something
no index can answer, so summing one week meant reading three years and calling
`date()` on each row: 72 ms, three quarters of a page render, on every screen
where time is entered.

`ANALYZE` was tried, because "the planner lacks statistics" is the standard
diagnosis. It moved running timers from 95 ms to 0.4 ms and the inbox from 98 ms
to **320 ms** — better and worse in the same run.

## Decision

**Write the query so that one index answers it exactly, and do not rely on the
planner choosing well.**

Concretely:

* Partial indexes whose predicate is *the condition the query states*, not an
  equivalent of it: `WHERE status = 'pending'` where the query says
  `status = 'pending'`.
* Comparisons against the bare column, with any arithmetic on the other side.
  `started_at >= ? || 'T00:00:00Z'` rather than `date(started_at) >= date(?)`.
  Timestamps are RFC 3339 UTC, whose lexicographic order is its chronological
  order — which is why that format was chosen (ADR-0015).
* `INDEXED BY` where a partial index is plainly right and the planner disagrees.
  It is an instruction rather than a hint, and dropping the index then breaks the
  query instead of quietly making it a scan.
* A count query for a count. The inbox badge was building every pending entry,
  with its assignment, project, customer, both users and an attachment count, to
  render a number.
* Bounded windows for "recent". Ranking eight assignments by grouping three years
  of history cost 170 ms; six weeks is what the screen already promised in words.

**And ANALYZE is not adopted.** Statistics make the plan depend on the shape of
the data, which is the kind of thing that is fine in testing and surprising in
production — as the inbox result showed within one run.

## Consequences

**Positive**

* Every ASR-012 budget is met: day view 19 ms, week 24 ms, timer start/stop
  18 ms, entries list 10 ms, one-year report 746 ms against a 2 s budget.

  The report figure deserves its context. It read 29 ms before pagination, and
  that number was false: the export was truncated at a thousand rows, so it was
  timing a thousandth of the work. A real one-year export of this dataset is
  about 33,000 entries. That cost 1.0 s when the report was assembled in memory
  and 746 ms once it streamed, which is the part of it that was never the
  database.

  The caveat this entry used to carry - that a multi-year export would not meet
  the budget - has been removed rather than restated, because the shape of the
  cost changed. A three-year export is no longer a bigger version of the same
  risk: it runs in about 2 s at a peak heap of 9 MB, where assembling it needed
  294 MB. The criterion still names one year; what a longer range does is now
  bounded rather than unknown.
* The suite exists, so the next regression of this kind is caught by a build
  rather than by a user.
* The queries no longer depend on the planner being clever, so behaviour does not
  change as a database grows.

**Negative / accepted costs**

* `INDEXED BY` couples one query to one index by name. Deliberate: a dropped
  index should fail loudly rather than degrade silently.
* Partial indexes only serve queries that state their predicate exactly, so a
  future query for "not confirmed" will not use the "pending" index. That is the
  trade for predictability, and the suite will show it.
* The entries list was 147 ms, rendering up to a thousand rows. It is paged at
  fifty now, which took it to 10 ms - and removing that cap turned up two things
  it had been hiding. It was part of the filter, and the filter was what every
  export was built from, so exports had been truncated at a thousand rows the
  whole time; and once they were not, the tag lookup exceeded SQLite's
  bound-parameter limit, which had never been reachable while something else
  capped the row count. A limit imposed for one reason was silently load-bearing
  for two others.
* The suite takes about a minute and is excluded from `make check`, so it is only
  as useful as the habit of running it. `make test-perf` exists for that.
* Streaming is a constraint on the formats, not a free improvement. The JSON
  schema can stream only because its totals follow its entries; the elapsed
  total can be folded in one pass only if the rows arrive in ascending order, so
  the export query orders opposite to the screen's listing and the accumulator
  refuses input that is not sorted. And PDF, DOCX and Markdown genuinely cannot
  stream, so they are bounded at 50,000 rows and say so - a limit where there
  had been none, which is the honest form of a limit that was previously just
  the machine's memory.

## Alternatives considered

**Run ANALYZE at startup, or `PRAGMA optimize` periodically.** The standard
advice, and it made one query 230× faster and another 3× slower. Rejected on the
evidence rather than on principle.

**Cache the page shell's queries.** The running timers, the badge and the week
banner are the same for a whole request and mostly the same between requests. A
cache would have hidden three scans instead of removing them, and cache
invalidation on a timesheet — where a stale total is a wrong number on screen —
is not a trade worth making for work that is now sub-millisecond.

**Denormalise a week total onto the period row.** It already exists as
`current_seconds`, and keeping it accurate as entries change is exactly the
consistency problem the query avoids. The query is 0.2 ms once it uses an index.

## Related

* ADR-0003 — pure-Go SQLite
* ADR-0015 — timestamps stored in UTC, displayed local
