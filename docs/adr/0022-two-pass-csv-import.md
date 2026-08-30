# ADR-0022: CSV import previews everything and imports all or nothing

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-019

## Context

People arrive with a spreadsheet. A month of hours from another system, a
colleague's timesheet, an export from a tool being replaced. Typing it in again
is what makes them not bother.

The naive implementation reads the file and inserts what it can. The failure mode
is the problem: 340 rows of 400 succeed, sixty are silently absent, and the user
now has a reconciliation problem rather than a timesheet. They cannot tell which
sixty without comparing both sources row by row - which is more work than typing
it in would have been.

## Decision

**Two passes, and all-or-nothing on the second.**

The first pass parses every row, matches it against the catalogue, and reports
every problem with the line number it occurred on. It **writes nothing**. The
user sees what would be imported, what would be created, and what cannot be
matched, before agreeing to anything.

The second pass runs only on confirmation, and imports every valid row **or
none**. A row that was invalid in the preview is refused outright rather than
skipped, so what gets imported is exactly what was shown.

Supporting decisions:

* **Column names are matched loosely** - `duration`, `hours`, `time`, `timmar`
  all mean the same thing - because the file comes out of somebody else's
  system and rejecting it over a header spelling is needless.
* **Ambiguous dates are refused, not guessed.** `03/04/2026` is 3 April or 4
  March depending on who exported it. Guessing silently puts a month of work on
  the wrong days; refusing names the value and is recoverable in a text editor.
* **Missing catalogue records are created only when asked**, and the preview
  lists exactly what would be created, so nobody discovers eleven new customers
  after the fact.
* **Times of day are not invented.** A CSV of daily totals has no start time, so
  entries are placed at the start of the working day. Overlapping entries are
  legal here ([ADR-0004](0004-concurrent-timers.md)), so several rows on one day
  do not conflict - and fabricating distinct start times would be inventing
  detail the file does not contain.

## Consequences

**Positive**

* An import either happened or did not. There is never a partial state to
  reconcile.
* Problems are reported against line numbers the user can find in their
  spreadsheet.
* The preview is a safe place to experiment: it writes nothing, so it can be run
  as many times as needed.

**Negative / accepted costs**

* **The file must be uploaded twice** - once to preview, once to commit. A
  browser cannot re-post a file it no longer holds, and keeping it server-side
  between the two requests would mean storing somebody's data before they agreed
  to import it. The extra step is the honest cost of not doing that.
* All-or-nothing means one bad row blocks 399 good ones. That is the intent, and
  the preview makes the bad row easy to find, but it will frustrate someone with
  a large messy file.
* ~~The import runs row by row rather than in one transaction, so a failure
  part-way through leaves earlier rows written.~~ **Done.** Every row is prepared
  first - which is where a refusal names a line in the user's spreadsheet - and
  the writes then happen in one transaction, together with each row's audit
  record and the summary row that says how many were imported. A failure part-way
  through now leaves nothing behind, and the summary cannot claim an import that
  did not happen. `TestAnImportThatFailsPartWayImportsNothing` injects a failure
  against the third row of three.
* The catalogue records created for an import are not part of that transaction.
  Creating a customer is an audited change of its own, and nesting transactions
  on the single write connection would deadlock. It is also the right split: they
  are what the preview listed and the user agreed to, they are matched by name,
  and a re-run after a failed import reuses them rather than making a second set.
  What must never be partial is the time.

## Alternatives considered

**Import what parses, report the rest** - what most tools do. Rejected on the
reconciliation problem above.

**Guess ambiguous dates from the majority of the file** - clever, and it would
usually be right. Rejected: "usually right" about which day work happened on is
not good enough when the answer goes on an invoice.

**Accept a fixed column layout** - simpler to implement and to document.
Rejected: every source system emits a different layout, and requiring the user to
rearrange their spreadsheet first defeats the purpose.

## Related

* ADR-0004 (overlapping entries), ADR-0021 (backups)
