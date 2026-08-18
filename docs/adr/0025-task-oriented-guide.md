# ADR-0025: A task-oriented guide alongside the per-screen help

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-022

## Context

The application already has context-sensitive help
([ADR-0020](0020-context-sensitive-help.md)): each screen declares its topics,
the `?` opens them, and the content lives in the message catalogues so it is
translated like everything else.

It answers one question well — *what am I looking at* — and it structurally
cannot answer another one: *how do I do X*.

Recording time for a colleague is the case that makes this obvious. The answer
involves the day view, an `@name` in the quick-add box, a shared project
membership, and a confirmation from the colleague before the time counts. Nobody
who has just been asked to "put Erik's hours in" knows to be on the day view, so
per-screen help never reaches them. Worse, the part that surprises people — it
is a *proposal*, not an entry — is a consequence of a design decision
([ADR-0005](0005-proxy-time-entry.md)) that no single screen explains.

The same gap applies to submitting a week, correcting a mistyped duration,
claiming mileage, and getting the data out for invoicing.

## Decision

**A second documentation surface, organised by task rather than by screen**, at
`/guide`.

The two are kept distinct and both are kept:

| | Context help (`?`) | Guide (`/guide`) |
|---|---|---|
| Question | what is this screen | how do I do X |
| Entry point | the screen you are on | the navigation, or a link |
| Organised by | screen | task |
| Length | a few paragraphs | numbered steps |

The guide's content lives in the message catalogues and renders through the same
restricted markup as the help, so it is translated, escaped, and cannot become a
second templating mechanism. The markup gained ordered and unordered lists for
this: steps are the natural shape of a how-to, and running them together as
prose makes an instruction that must be followed in order look optional.

**The whole guide is one page**, with a table of contents and an anchor per
topic. One URL to send somebody, the browser's own find works across all of it,
and it prints as a manual. Each topic also keeps its own URL so a link can land
on the answer rather than near it, and an unknown topic falls back to the whole
guide rather than 404ing — somebody who has reached a guide URL is asking for
instructions, and an error page is a poor answer to that.

**Topics that cannot apply are not offered.** In local mode there is nobody to
record time for and nobody to approve anything, so those topics are absent
rather than present-and-inapplicable. Documentation for a control that is not
shown sends people hunting for it.

## Consequences

**Positive**

* The question people actually arrive with has an answer, and it is one link
  away from every screen.
* The surprising parts of the design — proxy consent, the week lock, the empty
  end-time field — are explained where somebody meets them rather than only in
  an ADR.
* Being catalogue content, it is translated by the same process as the interface
  and the parity test fails the build if a language falls behind.

**Negative / accepted costs**

* **Two places to update.** A change to how something works now needs both the
  screen's help and the guide topic. They are deliberately not merged, because
  merging them would produce a manual that is too long to read on a screen and
  too shallow to follow as a procedure.
* **Prose in JSON.** The catalogue entries are long, and the file is now
  substantially documentation by volume. It keeps translation and escaping
  uniform, which is worth more than the tidiness of a separate format.
* **The guide can drift from the product** in a way code cannot. The tests check
  that every topic exists in every language and that specific promises are
  present, which catches an absent topic but not a stale sentence.
* The markup subset is still small — no headings within a topic, no tables, no
  links between topics. Adding them means growing a renderer that is deliberately
  restricted, so the topics are kept short enough not to need them.

## Alternatives considered

**Extend the per-screen help instead.** Rejected: it puts the answer behind
knowing which screen to be on, which is precisely what the person does not know.

**A single long manual page with no per-screen help.** Fewer places to update.
Rejected: the `?` answering "what is this column" in place is the more
frequently useful of the two, and losing it to gain a manual would be a net
loss.

**Ship the documentation as Markdown files served from disk.** Easier to write
and to review as a diff. Rejected: it would be untranslated, outside the
catalogue parity test, and a second rendering path with its own escaping story.

**A link to external documentation.** Rejected for the same reason the
application embeds its assets: an instance on a laptop with no network is a
supported deployment, and documentation that is only sometimes available is not
documentation.

## Related

* [ADR-0020](0020-context-sensitive-help.md) — the per-screen help this sits beside
* [ADR-0019](0019-message-catalogues-and-server-side-localisation.md) — where the
  content lives and how it is translated
* [ADR-0005](0005-proxy-time-entry.md) — the proxy consent model the guide explains
