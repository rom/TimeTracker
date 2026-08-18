# ADR-0019: Message catalogues, resolved on the server

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-016

## Context

The application needs to be usable in more than one language, starting with
English and Swedish. Three questions follow: where the translations live, who
resolves them, and how much localisation beyond strings is needed.

The third question is the one that is usually got wrong. Translating words while
still rendering `1.50` and `03/16/2026` produces an interface that reads as
broken to a Swedish user: their convention is `1,50` and `2026-03-16`, and a
decimal point in a timesheet figure is not a stylistic difference but a
different number.

## Decision

**JSON catalogues, embedded, resolved entirely on the server.**

One flat key-value file per language in `internal/i18n/locales`, embedded with
`go:embed` so the binary stays self-contained (ASR-003). A `Printer` is built
per request and carried on the page data, so a template writes `{{.T "nav.today"}}`
and no template needs to know how the language was chosen.

**Server-side, not client-side.** The alternative - shipping the catalogue to
the browser and translating in JavaScript - would mean the page arrives in one
language and is corrected in another, which is visible as a flash and is worse
than visible for a screen reader: the `lang` attribute would be wrong for the
moment the page is announced. Rendering the right language in the first byte
avoids both.

**Language resolution order:** the user's stored preference, then the browser's
`Accept-Language` header (with quality values parsed properly, since browsers
routinely send `en-GB,en;q=0.9,sv;q=0.8` and the first tag is not the answer),
then English. A stored preference wins because it is an explicit choice, and a
browser configured by an employer should not override what someone selected.

**Localisation extends past strings.** The `Printer` also formats decimals,
durations, money and dates. Swedish gets a decimal comma, a non-breaking space
as the group separator, and unit labels from the catalogue (`1 tim 30 min`, not
`1h 30m`). The formatters take the *already-formatted* string from the domain
layer rather than a number, so the exact integer arithmetic remains the single
source of the value and this only changes how it is written
([ADR-0014](0014-exact-money-and-duration.md)).

**Plurals are two-form.** `key.one` and `key.other`, which is correct for both
supported languages. A language with more categories would need real CLDR rules,
and `Printer.N` is the single function that would grow them.

## Consequences

**Positive**

* No flash of the wrong language, and the `lang` attribute is correct from the
  first byte - which matters because a screen reader chooses its voice from it.
* No client-side catalogue, so nothing to ship, cache or version separately.
* A parity test fails the build when a catalogue is missing a key, so a
  half-translated interface cannot ship quietly.
* Numbers and dates are right, not merely the words around them.

**Negative / accepted costs**

* Changing language is a round trip and a page reload. Acceptable: it is done
  approximately once per person.
* Every user-visible string must go through a catalogue, which is friction when
  adding a screen and is the discipline most likely to slip. The test that scans
  rendered pages for leaked keys catches the common form of the mistake, but not
  a hard-coded English sentence.
* Two-form plurals are not enough for Polish, Russian or Arabic. Adding one of
  those means implementing CLDR plural categories, not just writing a catalogue.
* Flat keys have no namespacing beyond a dotted convention, so the catalogue
  will need discipline as it grows.

## Alternatives considered

**`golang.org/x/text/message` with generated catalogues** - the canonical Go
answer, with real CLDR plural rules and number formatting. Rejected as
disproportionate: it adds a code-generation step to a build that deliberately has
none, and its formatting is harder to override where this application wants a
specific convention.

**Client-side translation** - allows switching language without a reload.
Rejected on the flash and the `lang` attribute, both of which are accessibility
problems rather than cosmetic ones.

**Translate strings but not numbers or dates** - much less work. Rejected: it is
the half that makes an interface read as broken, and a decimal point where a
comma belongs is a wrong number on a timesheet, not a style choice.

## Related

* ADR-0014 (exact arithmetic), ADR-0020 (context-sensitive help)
