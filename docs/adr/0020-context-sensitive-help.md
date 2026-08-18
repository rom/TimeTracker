# ADR-0020: Help is context-sensitive, translated, and works without JavaScript

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-017, ASR-016

## Context

Several of this application's behaviours are deliberate and surprising. Timers
overlap and are never split automatically. Two totals are shown and they
disagree. Quick add refuses to guess. Rates are frozen onto an entry when it is
billed. Each of those is defensible, and each looks like a bug to someone
meeting it for the first time.

A manual in a repository does not help that person: they are looking at a screen
with two different totals on it and wondering which one is wrong.

## Decision

**Help is attached to screens, not collected in a manual.**

Each screen declares which topics are useful there, in an explicit map rather
than derived by rule - the day view needs the quick-add syntax more than it needs
the role model, and no rule can infer that. The help panel shows those topics.

**Help content lives in the message catalogues**, so it is translated exactly
like every other string ([ADR-0019](0019-message-catalogues-and-server-side-localisation.md))
and a Swedish user gets Swedish help rather than an English wall of text.

**The help control is a real link to `/help/<screen>`.** With JavaScript
disabled the browser navigates there and gets a whole page; with script the same
fragment is loaded into a panel without losing the user's place. One template
definition serves both, so they cannot drift apart.

**Focus is managed.** Opening the panel moves focus to its heading; closing
returns focus to the control that opened it. A panel that appears without focus
is invisible to a screen reader user, and one that closes without returning focus
loses their place entirely.

**A restricted markup subset**, not Markdown: blank lines separate paragraphs,
`**bold**` emphasises, and backticks mark literal input. Everything is
HTML-escaped *first* and the markup applied afterwards, so a catalogue string -
written by a translator, not a programmer - can never inject markup even though
the rendered result is marked as trusted HTML.

## Consequences

**Positive**

* The explanation is one keystroke from the thing being explained.
* Translated help, for free, by the same mechanism as everything else.
* Works without JavaScript, and is usable with a keyboard and a screen reader.
* Three constructs of markup rather than a Markdown dependency: less code, and
  a much smaller surface to get the escaping wrong in.

**Negative / accepted costs**

* Help text lives in JSON catalogues, where prose is awkward to write and to
  review - escaped newlines, no syntax highlighting, and a diff that is hard to
  read.
* The screen-to-topic map is maintained by hand and will fall out of step when a
  screen is added; nothing fails the build when it does.
* The markup subset will be asked to grow - lists first, then links - and each
  addition is escaping code to get right.
* Help duplicates explanations that also exist in `docs/`, so the two can
  disagree. The help is the user-facing one and the ADRs are the reasoning; that
  division is a convention, not something enforced.

## Alternatives considered

**A single manual page** - simplest. Rejected: it makes the reader find their
own question, which is exactly the work they came for help to avoid.

**Tooltips on individual controls** - closest to the point of confusion.
Rejected as the primary mechanism: tooltips cannot hold the two paragraphs that
"why are there two totals" actually needs, and they are notoriously awkward on
touch devices. Field-level hints are used where one sentence does suffice, tied
to their input with `aria-describedby`.

**Render Markdown from the docs directory** - one source of truth. Rejected:
`docs/` explains *why* decisions were made, for a developer, and is not what a
user needs while looking at a timesheet.

**A guided tour on first run** - rejected: it explains everything at the moment
the user knows least, and nothing at the moment they are confused.

## Related

* ADR-0019 (message catalogues), ADR-0011 (theming)
