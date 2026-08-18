# ADR-0011: Theming via CSS custom properties only

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-009, ASR-011

## Context

Seven themes are required: light, dark, gold, sand, spring, autumn and high
contrast. Seven is the number at which the naive approaches collapse — per-theme
stylesheets means seven files to keep in sync every time a component changes, and
conditional classes in templates means every component knows about every theme.

There is also a correctness dimension. Assignments and projects carry user-chosen
colours (ADR: see DESIGN.md), and a colour that reads well on a light background can
be invisible on a dark one. And the high-contrast theme has an objective bar to
clear (WCAG 2.1 AA), not merely an aesthetic one.

## Decision

**One stylesheet. One set of semantic tokens. Themes redefine only the tokens.**

```css
:root { --surface: …; --text: …; --accent: …; --border: …; --danger: …; }
[data-theme="dark"]   { --surface: …; --text: …; }
[data-theme="autumn"] { … }
```

Rules:

* Components reference **semantic** tokens (`--surface-raised`, `--text-muted`,
  `--accent`), never literal colours and never palette-named tokens like
  `--gold-400`. A component that says `color: var(--text-muted)` is automatically
  correct in all seven themes.
* Switching theme sets `data-theme` on `<html>` and changes nothing else — no class
  churn, no re-render, no second stylesheet request.
* Preference is stored per user (server mode) or in local storage (local mode), and
  applied inline in the document head before first paint so there is no flash of the
  wrong theme.
* The default follows `prefers-color-scheme` until the user chooses explicitly.
* **User-chosen entity colours** (assignment/project) are stored as a palette *key*,
  not a hex value. Each theme maps keys to concrete colours that work on its own
  background, so "Acme is blue" stays legible in every theme. Foreground colour for
  a coloured chip is computed for contrast rather than fixed.
* Icons are a curated set rendered as inline SVG using `currentColor`, so they
  re-colour with the theme; user-visible symbols may also be emoji, which are
  theme-independent by nature.
* The high-contrast theme is held to WCAG 2.1 AA by an automated contrast test over
  every token pair the components actually use (see TEST.md), so a future token edit
  cannot silently break it.

## Consequences

**Positive**

* Adding an eighth theme is one block of token definitions, no component changes.
* No flash of unstyled or wrongly-themed content.
* Accessibility is verified by a test rather than asserted in a README.
* Works with JavaScript disabled apart from the toggle itself (server mode persists
  the choice server-side).

**Negative / accepted costs**

* Theme authors must work within the fixed token vocabulary; a theme that wants a
  structurally different look (e.g. different border radii or spacing) needs new
  tokens added to the vocabulary, touching all themes.
* Indirection: reading a component's CSS does not tell you what colour it is
  without consulting the token table.
* Storing entity colours as palette keys means users pick from a palette rather than
  an arbitrary colour wheel. This is a real limitation, accepted because arbitrary
  hex values cannot be made legible across seven backgrounds.

## Alternatives considered

**One stylesheet per theme** — simple to reason about individually. Rejected: seven
copies to keep in sync, seven times the chance of drift, and switching themes
becomes a network request.

**Tailwind-style utility classes with a dark variant** — well-trodden. Rejected: it
handles two themes gracefully and seven awkwardly, and it needs a Node build step,
against ASR-003.

**CSS `filter: invert()` for the dark theme** — cheap. Rejected: it wrecks images,
attachments and entity colours, and produces exactly one extra theme.

## Related

* ADR-0002 (server-rendered UI)
