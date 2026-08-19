# ADR-0029: Trigram full text, with a scan fallback and optional regular expressions

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-025

## Context

People look for entries by fragments of what they typed. "redir" to find "fixed
the login redirect"; "acme" to find a customer; "incident" to find a tag. The
searches are short, unanchored, and usually in the middle of a word.

SQLite's default FTS5 tokenizer indexes whole words, so `redir` finds nothing.
Prefix queries (`redir*`) find words *starting* with it, which is closer and
still wrong for the common case. `LIKE '%redir%'` finds it and scans every row,
which is fine on a laptop with a year of data and not fine on a shared instance
with a decade of it.

Separately, some users want regular expressions, and SQLite defines the `REGEXP`
operator while shipping no implementation of it: `x REGEXP y` is a "no such
function" error until something registers one.

## Decision

**Three mechanisms, chosen per query, and the choice is reported back.**

1. **FTS5 with the `trigram` tokenizer** for ordinary search. A trigram index
   makes unanchored substring matching indexed rather than linear, which is
   exactly the shape of the queries people type.
2. **`LIKE`** when the query is shorter than three characters. A trigram index
   cannot look up a fragment shorter than a trigram, so FTS5 would return
   *nothing* — not everything, nothing — and a two-character search would
   silently produce an empty page. The fallback is not an optimisation; it is
   what stops the feature lying.
3. **`REGEXP`**, registered against the driver, when the user asks for it.

**The mechanism used is shown on the screen.** Two similar searches behaving
differently is confusing; two similar searches behaving differently *and saying
which one used the index* is a tool somebody can reason about.

Supporting decisions:

* **The query is always literal.** FTS5 has its own query language, with `AND`,
  `OR`, `NOT`, `NEAR`, `*` and `^` as operators. A user typing `C++`, `R&D` or a
  stray quotation mark means those characters. The query is wrapped as a
  quoted FTS5 string, so a search is a search rather than an accidental
  expression.
* **Regular expressions are RE2**, because that is what Go's `regexp` is: linear
  time, no backtracking. A pathological pattern from a user cannot hang the
  process the way it could with a PCRE engine. That property is most of why
  exposing regular expressions to users is defensible at all.
* **Patterns are case-insensitive by default.** Somebody typing into a search
  box is searching, not writing a program; `(?-i)` is available for the case
  where they are.
* **The index is maintained by the application**, in the same transaction as the
  entry write, rather than being an FTS5 external-content table. The searchable
  text spans four joined tables plus a tag list, and no trigger could keep that
  right. A rebuild is offered for the paths that write entries without going
  through the normal one — a restore, mainly.
* **A malformed pattern is the searcher's mistake**, and reports as a validation
  failure naming what is wrong with it rather than as an internal error.

## Consequences

**Positive**

* The search people actually type is fast and correct.
* Nothing silently returns an empty page.
* A regular expression is available without becoming the default, and cannot be
  used to hang the server.

**Negative / accepted costs**

* **A trigram index is large** — several times the text it covers. For a
  timesheet's notes this is small in absolute terms, but it is real, and it
  grows with history rather than with activity.
* **Regular-expression search is a full scan**, and always will be: no index can
  serve an arbitrary pattern. It is offered because somebody who asks for it
  usually knows what they are asking for, and it is not the default.
* **The index can drift.** It is maintained in code rather than by the database,
  so a bug in a new write path would leave entries findable by every route
  except search. The rebuild is the repair; there is no automatic detection.
* **Registering a driver function is process-global.** A second registration
  would fail, so it happens in an `init` and panics if the driver has changed
  underneath us — noisy at startup rather than confusing at query time.
* Tag renames trigger a full reindex. Cheap relative to how rarely tags are
  renamed, and wrong to skip: the index carries tag names.

## Alternatives considered

**Prefix indexes (`prefix='2 3'`)** instead of trigram. Smaller, and they handle
`redir*`. Rejected: they do not match the middle of a word, which is where the
useful fragments are.

**`LIKE` alone.** Simple, no index, no schema. Rejected on the shared-instance
case — and the trigram index exists precisely so that the honest fallback is
needed only for queries too short to index.

**Expose FTS5's query language to users**, so `login NOT redirect` works.
Rejected: the failure mode is a user searching for a customer called `AND` and
getting a syntax error, or worse, silently different results.

**A regular expression as the only mode.** Powerful and hostile. Most searches
are three letters typed in a hurry.

## Related

* [ADR-0003](0003-pure-go-sqlite.md) — the driver, which determines that FTS5
  and the trigram tokenizer are available and that REGEXP is not
* [ADR-0022](0022-two-pass-csv-import.md) — the other place a mechanism is
  reported rather than assumed
