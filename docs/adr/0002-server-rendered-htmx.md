# ADR-0002: Server-rendered HTML with HTMX, no SPA

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-003, ASR-011, ASR-012

## Context

The UI needs live-updating timers, inline editing of timesheet rows, drag-free quick
entry, clipboard paste of images, and seven themes. That is genuinely interactive —
enough that a single-page application is a defensible choice.

Against that: ASR-003 requires a single self-contained binary with no Node.js
toolchain, and ASR-011 requires code a human can read. A React/TypeScript frontend
adds a second language, a package manager, a bundler, a lockfile, a transpiler
config, and roughly a doubling of the code to review — plus a JSON API surface that
must be secured and versioned independently of the pages that consume it.

## Decision

We will render HTML on the server with Go's `html/template`, and use **HTMX** for
partial updates: a request from a fragment of the page returns a fragment of HTML,
which HTMX swaps into place. Hand-written vanilla JavaScript covers the few things
HTMX does not: the ticking clock on running timers, clipboard paste interception,
theme switching, and keyboard shortcuts. There is no build step; HTMX ships as a
vendored file embedded in the binary.

Handlers detect the `HX-Request` header and render either a full page or the
relevant fragment from the same template set, so every screen works with JavaScript
disabled — degraded, but functional.

## Consequences

**Positive**

* `make build` is `go build`. No Node, no lockfile, no supply chain from npm.
* One language, one mental model, one place where authorisation is enforced (the
  handler that renders the fragment).
* State lives in the database, not duplicated in a client store, so there is no
  client/server sync bug class.
* Rendering a fragment costs a query and a template execution, which comfortably
  meets the 100 ms budget in ASR-012.

**Negative / accepted costs**

* Every interaction is a round trip. On a laptop or a LAN this is imperceptible; over
  a poor connection to a remote server it will feel less fluid than an SPA with
  optimistic updates. Accepted: the running-timer clock is client-side precisely so
  the most latency-sensitive element does not round-trip.
* Rich client-side interactions we might want later (drag-to-resize entries on a
  calendar grid) will need real JavaScript, written by hand.
* Template fragments must be carefully factored, or full-page and partial renders
  will drift apart.

## Alternatives considered

**React/TypeScript SPA + JSON API** — best interactivity ceiling. Rejected on
ASR-003: embedding a compiled bundle is possible, but *producing* it requires a Node
toolchain that every contributor and CI job must install, and the JSON API doubles
the authorisation surface.

**Templates + Alpine.js, classic form posts** — simplest of all, no HTMX dependency.
Rejected because starting or stopping a timer would reload the whole page, which is
the single most frequent interaction in the app.

**Go WebAssembly frontend** — one language, no Node. Rejected: multi-megabyte
payloads, poor DOM ergonomics, and a debugging story that fails ASR-011 badly.

## Related

* ADR-0009 (embedded assets), ADR-0011 (theming)
