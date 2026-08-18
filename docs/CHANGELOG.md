# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Architecturally significant changes reference the [ADR](adr/README.md) that records
the decision.

## [Unreleased]

### Fixed

* **A completed form was rejected as empty.** Creating a customer through the
  browser failed with "customer name is required" even with every field filled
  in. `fetch(FormData)` sends `multipart/form-data`; `r.ParseForm` does not parse
  a multipart body but *does* set `r.Form`, so the later `r.FormValue` never fell
  back to the multipart parser and every field arrived empty. Form decoding now
  goes through one helper that picks the parser from the content type, and the
  client sends URL-encoded bodies so the JavaScript and no-JavaScript paths are
  handled by identical code. Covered by a regression test that submits the same
  form in both encodings.

### Added

* **Server mode** (layer 2 of [MVP_PLAN.md](MVP_PLAN.md)). `--mode=server` now
  runs a real multi-user service.
  * **Local accounts** with Argon2id hashing (OWASP parameters, stored inside the
    hash so they can be raised later and upgraded transparently on next login),
    constant-time verification, and a uniform failure response so the login form
    cannot be used to discover which accounts exist.
  * **OIDC single sign-on** — Authorization Code with PKCE, `state` and `nonce`
    verified, ID token signature checked against the provider's JWKS with an
    RS256 allow-list, issuer, audience and expiry validated. Accounts link on the
    immutable `sub` claim, never on email.
  * **Server-side sessions** with an opaque cookie whose SHA-256 is what gets
    stored, `HttpOnly`/`Secure`/`SameSite=Lax`, host-only, idle *and* absolute
    lifetimes, and immediate revocation on sign-out, password change, role change
    or account disablement.
  * **CSRF protection** on every unsafe request, with the token bound to the
    session and carried both as a hidden field and a header, so the
    no-JavaScript path is protected too.
  * **Login rate limiting** per account *and* per source address, with capped
    exponential backoff.
  * **RBAC**: admin, manager, member and client, scoped by project membership,
    enforced at one point in the service layer and covered by an exhaustive
    role × action × scope test matrix.
  * **Scoped listing queries** ([ADR-0016](adr/0016-scoped-listing-queries.md)) —
    the store offers no unscoped list, and the empty scope renders as "match
    nothing", so a forgotten scope shows an empty screen rather than everyone's
    data.
  * **rsyslog forwarding** in RFC 5424 through a bounded non-blocking queue, so a
    dead collector can never block or fail a user's request; drops are counted.
  * **Per-person project rates**, the level between the assignment and the
    project in rate resolution.
  * **Users screen** for accounts, roles and project access, and a first-admin
    bootstrap that refuses once any account exists.

* **Working local-mode application** (layer 1 of [MVP_PLAN.md](MVP_PLAN.md)).
  `bin/timetracker` starts, migrates its own database, and serves the UI on
  loopback with no configuration.
  * **Domain layer** — `Money` as integer minor units with currency checking,
    duration parsing (`1.5`, `1h30`, `90m`, `1:30`), rounding rules applied at
    one documented point, entity types and validation, interval union
    arithmetic.
  * **Storage** — SQLite through the pure-Go driver with WAL, foreign keys and a
    single write connection; embedded forward-only migrations with checksum
    verification, a newer-schema guard and a pre-migration backup.
  * **Service layer** — the single enforcement point: every method consults the
    authoriser, every mutation writes its audit row inside the same transaction
    as the change.
  * **Concurrent timers** — any number may run at once; overlapping intervals
    are recorded and reported, never auto-split. Stopping is idempotent, and a
    timer past the configured maximum is flagged for review rather than billed.
  * **Totals** — summed and elapsed reported side by side wherever entries can
    overlap, with the overlap stated explicitly.
  * **Billing** — layered rate resolution (assignment → project → customer →
    instance default) with the resolved rate and applied rounding rule stored on
    the entry, so a later rate change cannot rewrite an invoiced amount.
  * **Screens** — Today (timeline, gaps, quick add, one-click start), Week
    (assignment × day grid), Entries (filterable, the basis of every export),
    Admin (customers, projects, assignments with colours and icons).
  * **Quick add** — `2h acme/migration fixed the login redirect #travel`, with
    ambiguity reported rather than guessed.
  * **Gap detection** — unaccounted stretches surfaced on the day view as
    prompts, never filled in automatically.
  * **Seven themes** — light, dark, gold, sand, spring, autumn and high
    contrast, as redefinitions of one semantic token set, applied server-side
    before first paint.
  * **Exports** — CSV (UTF-8 with BOM so Excel reads it correctly) and JSON
    against a versioned schema, both rendered from one `Report` value so they
    cannot disagree.
  * **Keyboard shortcuts**, live client-side timer clocks, and a background
    submit layer, all degrading to plain form posts without JavaScript.
* **Tests** — domain, store, service, HTTP and architecture suites, including a
  test that fails the build if `internal/web` ever imports `internal/store`, and
  one that checks every theme defines every semantic token.

* **Documentation set.** `MVP_PLAN.md`, `DESIGN.md`, `ARCHITECTURE.md`, `TEST.md`,
  `SECURITY.md` and this changelog.
* **Architecturally Significant Requirements register** ([ASR.md](ASR.md)) — 14
  requirements, each with a quality attribute, an objective fit criterion, a
  rationale and the ADRs that realise it.
* **Architecture Decision Records** ([adr/](adr/README.md)) — 15 accepted records
  with an index, a template and the process rules (one decision per record,
  immutable once accepted, superseded rather than edited):
  * ADR-0001 single binary with two run modes
  * ADR-0002 server-rendered HTML with HTMX, no SPA
  * ADR-0003 SQLite through a pure-Go driver
  * ADR-0004 multiple concurrent timers, overlaps allowed
  * ADR-0005 proxy entries require the subject's confirmation
  * ADR-0006 local accounts plus OIDC, session cookies
  * ADR-0007 pure-Go PDF and DOCX generation
  * ADR-0008 four roles scoped by project membership
  * ADR-0009 embedded assets and forward-only migrations
  * ADR-0010 append-only audit log, rsyslog via RFC 5424
  * ADR-0011 theming via CSS custom properties only
  * ADR-0012 layered packages with a service boundary
  * ADR-0013 content-addressed attachments on disk
  * ADR-0014 integer minor units and whole seconds
  * ADR-0015 timestamps stored in UTC, displayed local
* **Makefile** producing `bin/timetracker`, with cross-compilation for
  macOS/Linux/Windows on amd64 and arm64, plus test, lint, vet, fmt, coverage and
  clean targets.
* Go module and layered package skeleton.

### Known gaps

* **TOTP and API tokens are designed for but not built.** Neither is
  load-bearing for a small team behind SSO, and both are additive.
* **PDF and DOCX export return 501.** Both arrive in layer 5. CSV and JSON are
  complete.
* **The HTMX library is not vendored yet.** The templates use HTMX's attribute
  vocabulary, and `static/js/app.js` implements the subset the application
  relies on (`hx-post`, `hx-confirm`, and the `HX-Request`/`HX-Refresh` header
  protocol). Dropping the upstream library in its place needs no template
  changes. Until then the tree contains no third-party JavaScript at all.
* Attachments, expenses, proxy entries, tags and approvals are designed for but
  not implemented; see [MVP_PLAN.md](MVP_PLAN.md).

[Unreleased]: https://github.com/rom/timetracker/commits/main
