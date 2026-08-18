# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Architecturally significant changes reference the [ADR](adr/README.md) that records
the decision.

## [Unreleased]

### Fixed

* **The header clock showed `--:--:--` and never updated.** Three defects, one
  symptom. The value was a placeholder for JavaScript to replace, so a script
  that never ran, one that failed before reaching it, and a broken clock were
  indistinguishable and all silent — the time is now rendered by the server, so
  the worst case is a stale clock rather than a visibly broken one, and it works
  with no script at all. It read the *browser's* zone while every other time on
  the page used the user's. And `init()` ran seven features in sequence with no
  isolation, so the first to throw silently disabled the six after it; the clock
  was second, which made almost any early fault produce exactly this symptom.
  Each feature now starts independently and a failure is logged.
* **The inbox returned 500 as soon as it held a proposal.** A custom template
  function named `index` shadowed the Go template builtin, so an ordinary map
  lookup failed with "wrong type for value; expected []int64" — an error that
  says nothing about the cause. The expenses screen would have done the same.
  Renamed to `nth`; both screens have regression tests that keep a row on the
  page, since the fault only appears when there is one.
* **The service layer kept its own copy of the time-entry insert and update.**
  The copies had already drifted — the transactional update never wrote
  `time_zone` — and a newly added column was written by one and not the other,
  so a field the application believed it was storing was not stored at all.
  There is now one statement each, in the store, with the service calling the
  transactional form.
* **Editing an entry was implemented but unreachable.** The service, the routes
  and the form all existed; no screen ever rendered a link to any of them, so
  the only way in was to type a URL. Rows on the day and entries screens now
  carry an edit control, and a regression test asserts the link rather than the
  handler - the handler was never the broken part.
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

* **Customer contract terms: overtime, travel time and reimbursement.** Overtime
  and travel are a *kind* on the entry, chosen by the person; the customer's
  rules say what each is worth. Nothing is reclassified automatically — whether
  an hour counts as overtime is a contractual judgement, and a threshold that is
  exceeded produces a notice on the week view rather than a rate nobody agreed
  to. Travel a customer does not pay for is its own state, not a zero rate: the
  time is recorded in full and carries no amount. Reimbursement covers a default
  markup, mileage per kilometre and per diem per day — a quantity is priced from
  the customer's rate, with the working shown on the claim — and a receipt
  threshold enforced when the week is submitted. The kind reaches CSV, JSON, PDF
  and DOCX, because a line billed at one and a half times the base rate has to
  say why on the document carrying the figure. See
  [ADR-0024](adr/0024-customer-rate-rules.md).
* **An approval status report**, per person per week. The queue answers "what is
  waiting for me"; this answers "who has not submitted", which no queue can show
  because a submission that was never made has no row. Weeks with time recorded
  and nothing handed in are marked; weeks nobody worked stay blank, so the cells
  that matter are not buried. Scoped like everything else: without the manage
  permission it is your own weeks.
* **A task-oriented guide** at `/guide`, alongside the per-screen help. Ten
  how-tos in numbered steps — recording time, fixing an entry, moving time,
  **recording time for a colleague**, submitting a week, approving one, expenses,
  exporting, backup, and setting up customers and contract terms. It exists
  because per-screen help cannot reach somebody who does not yet know which
  screen they need, which is exactly the position of anyone asked to put a
  colleague's hours in. Catalogue content, so translated and escaped like the
  rest; topics that cannot apply in the running mode are not offered. See
  [ADR-0025](adr/0025-task-oriented-guide.md).
* **Weekly submit and approve.** A person declares a week finished; a manager
  accepts it or sends it back with a reason; an approved week can be reopened,
  deliberately and audibly. Submitting locks the week - creating, editing,
  moving or deleting time in it is refused, as is starting a timer inside it or
  stopping one into it - and **an administrator is not exempt**, because a lock
  the most privileged user walks through is not a lock. Nobody decides on their
  own timesheet whatever their role, and the single-user build refuses the
  action outright rather than offering a control that means nothing. Enforcement
  is one function that every mutation calls; the tests prove that disabling it
  fails every one of those paths. See
  [ADR-0023](adr/0023-week-as-the-unit-of-approval.md).
* **Correcting an entry that is already recorded.** A screen of its own for the
  mistake this exists to fix: eight minutes typed where eight hours were meant.
  The duration is shown in the notation it accepts (`8m`, `8h`, `1h 30m`) beside
  the day and the start time, so a wrong value is legible as wrong. The end-time
  field is deliberately left empty: an end time takes precedence over a
  duration, so a prefilled one would silently discard the correction just typed.
  Every change is audited with the previous value.
* **PDF and DOCX export**, both generated in-process. The PDF writer is
  hand-written against the standard 14 fonts; its cross-reference table is
  verified by an independent parse in the tests, which is what a strict reader
  checks. DOCX is OOXML written into a zip with the standard library.
* **Attachments** on time entries and expenses: a content-addressed blob store
  (identical files stored once), uploads size-capped and type-sniffed rather
  than trusted, SVG refused as script-capable, the extension required to agree
  with the content, and every byte served through an authorising handler.
  Pasting an image from the clipboard attaches it, through the same endpoint and
  the same checks as a file input.
* **Expenses**, keeping billable and reimbursable as independent questions
  throughout, with markup on the billable side only and the billed amount frozen
  when recorded.
* **Proxy entries**, completing the consent workflow: time proposed for a
  colleague counts for nothing until they accept it, only the subject can
  decide, a rejection is kept with its reason, and overlapping proposals are
  flagged.
* **CSV import of hours** — previews every row with its problems and writes
  nothing, then imports all or none ([ADR-0022](adr/0022-two-pass-csv-import.md)).
  Ambiguous dates are refused rather than guessed.
* **Backup and restore** as a single JSON file, whole or partial by customer,
  project or date range, merging rather than replacing so a mistaken restore
  cannot lose data, with optional scheduled backups
  ([ADR-0021](adr/0021-json-backups-that-merge.md)).
* **A YAML configuration file**, found automatically or named with `--config`,
  plus `--verbose` and `--debug`.
* **Minimum 2h/4h/8h rounding presets**, alongside the increment rules.
* **Editable customers, projects and assignments**, one-click favourites, and
  **moving time between assignments** — across projects and customers — with the
  billing snapshot recomputed from the target and the move recorded in the audit
  trail.
* **Rate inheritance from the customer** when neither the assignment nor the
  project sets one.
* **Administrator toggles** for the header clock and the current date.
* **Regression tests** for every defect that reached a running build, and
  `make coverage-check` with per-package floors, wired into `make check`.

* **HTTPS, certificate scripts and platform hardening.**
  * TLS terminated by the application itself (`--tls-cert`, `--tls-key`), with a
    TLS 1.2 floor and 1.2 suites restricted to AEAD constructions with forward
    secrecy. An optional listener redirects plain HTTP with a 308. HSTS
    available but off by default, because it is hard to undo.
  * Server mode refuses to bind a non-loopback address without TLS, an upstream
    TLS declaration, or an explicit `--allow-insecure`. A group- or
    world-readable private key is refused with a message saying how to fix it.
  * `scripts/gen-cert.sh` and `gen-cert.ps1` generate ECDSA P-256 certificates
    with proper SANs and constrained key usage, self-signed or from a local CA.
  * **Landlock** self-sandboxing on Linux (`--hardening=auto|enforce`),
    implemented against the raw syscalls and trimmed to the kernel's ABI
    version. Off by default; the shipped systemd unit enables it.
  * `deploy/` carries a hardened systemd unit, an AppArmor profile, an SELinux
    policy module, a launchd job with a `sandbox-exec` profile, and a Windows
    service installer using a virtual account with a restricted token.
  * `scripts/harden-check.sh` reports what is actually active for a running
    instance.

* **Internationalisation, with Swedish.**
  * Embedded JSON message catalogues, resolved entirely on the server so the
    correct language and `lang` attribute are present in the first byte.
  * Localisation beyond strings: Swedish renders `1 234,50` and `1 tim 30 min`
    where English renders `1,234.50` and `1h 30m`, with locale-aware dates,
    weekday and month names, and money.
  * Language chosen from the user's stored preference, then `Accept-Language`
    with quality values parsed properly, then English.
  * A parity test fails the build when a catalogue lacks a key, and every screen
    is rendered in every language and scanned for leaked keys.

* **Accessibility.**
  * A skip link, labelled landmarks, `aria-current` on the current page, an
    accessible name on every form control, `aria-live` on the totals, and text
    alternatives wherever colour or an icon carries meaning.
  * The high-contrast theme is verified against WCAG 2.1 AA by computing real
    contrast ratios from the stylesheet — and the contrast maths is itself
    checked against known reference points.

* **Context-sensitive help.**
  * Each screen has its own help topics, translated like everything else,
    covering the behaviours that are deliberate but surprising: overlapping
    timers, the two totals, the quick-add parser's refusal to guess, frozen
    rates.
  * A real link, so it works without JavaScript; with script it loads into a
    panel that manages focus in both directions.

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

* **A backup carries attachment metadata but not the bytes.** The blobs live on
  disk and must be copied separately. Stated in the help text and in
  [ADR-0021](adr/0021-json-backups-that-merge.md); a zip container holding both
  is the obvious fix.
* **CSV import writes row by row rather than in one transaction.** The preview
  makes a mid-import failure vanishingly unlikely, but it is not impossible.
* **The PDF layout engine is minimal**: text, rules, tables and page breaks. No
  images, no embedded fonts. Its output is structurally verified but has not
  been opened in a viewer here — see the manual checks in
  [TEST.md](TEST.md) §5.
* **Weekly submit-and-approve** is still outstanding from layer 4, as are budget
  burn reporting and the narrowed `client` projection from layer 5.
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
