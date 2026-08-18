# MVP Plan

> Delivery is layered. Each layer ends with something that builds, runs, is tested
> and is reviewable, so the design can be corrected before the next layer is built
> on top of it.

## Definition of the MVP

**The smallest thing that a consultant would actually use to bill a client**, run
locally:

> Track time against multiple customers, projects and assignments — with several
> timers running at once — see the day and the week, and export what happened as
> CSV or JSON.

Everything else (auth, attachments, expenses, proxy entries, PDF/DOCX, automation)
is a later layer. Each is designed for in [DESIGN.md](DESIGN.md) and
[ARCHITECTURE.md](ARCHITECTURE.md) now, so nothing in Layer 1 has to be undone to
add it.

## Layer status

| Layer | Contents | Status |
|---|---|---|
| **0** | Documentation, ADR/ASR set, Makefile, project skeleton | ✅ delivered |
| **1** | Local-mode MVP: domain, storage, timers, day/week, themes, CSV/JSON | ✅ delivered |
| **2** | Server mode: auth, RBAC, sessions, rsyslog | ✅ delivered |
| **3** | Attachments, paste, expenses | ⬜ planned |
| **4** | Proxy entries and approval workflow | ⬜ planned |
| **5** | Reports, PDF and DOCX export | ⬜ planned |
| **6** | Semi-automatic assistance | ⬜ planned |
| **7** | Hardening, packaging, performance | ⬜ planned |

---

## Layer 0 — Foundations ✅

* `docs/`: MVP_PLAN, DESIGN, ARCHITECTURE, TEST, SECURITY, CHANGELOG.
* `docs/ASR.md` — 14 architecturally significant requirements with fit criteria.
* `docs/adr/` — 15 accepted decision records, indexed, with a template.
* `Makefile` producing `bin/timetracker`, plus cross-compilation, test, lint, fmt,
  vet, coverage and clean targets.
* Go module, package skeleton per [ADR-0012](adr/0012-layered-package-structure.md).

**Done when:** `make build` produces a runnable binary and `make test` passes.

---

## Layer 1 — Local-mode MVP ✅

The vertical slice. Everything here is reachable from the UI.

**Domain** (`internal/domain`)
Customer, Project, Assignment, TimeEntry, Tag, User; `Money` (integer minor units,
currency-checked) and duration helpers; validation rules; rounding rules with
boundary tests. No I/O, fully unit tested.

**Storage** (`internal/store`)
SQLite via `modernc.org/sqlite` with WAL, foreign keys, busy timeout; embedded
forward-only migrations with checksums and a pre-migration backup; CRUD and the
day/week/range queries; the indexes named in ARCHITECTURE §5.

**Services** (`internal/service`)
Timer start/stop (concurrent, idempotent stop, conditional update); manual and
edited entries; the permissive local authoriser behind the same `Can()` interface
the RBAC one will implement; summed-vs-elapsed totals; gap computation.

**Web** (`internal/web`)
Today, Week, Entries, Admin (customers/projects/assignments/tags) screens; HTMX
fragments for the running-timer header, entry rows and totals; quick-add parser;
form validation with inline errors; CSRF plumbing present from the start even though
local mode does not need it.

**Presentation**
The seven themes as token sets, theme switcher with pre-paint application, entity
colour palette keys, curated icon set, keyboard shortcuts, responsive layout.

**Export**
CSV and JSON from the shared `Report` value.

**Done when:** a user can create a customer, project and assignment; run three
timers at once; correct a duration by hand; see the day and week with both totals;
switch to any theme; and export a date range as CSV and JSON — all from one binary
with no configuration. **All of this now works.**

Delivered beyond the original slice: layered rate resolution with the billing
snapshot stored per entry, gap detection, the quick-add parser, long-timer
flagging, and the audit trail (written from layer 1 so that no mutation path is
ever added without one).

---

## Layer 2 — Server mode ✅

Local accounts (Argon2id) and OIDC/PKCE; server-side sessions with rotation, idle
and absolute lifetimes; CSRF enforcement; login rate limiting and lockout; optional
TOTP; API tokens. The four roles and project membership behind the same `Can()`.
Append-only `audit_event` written inside each mutation's transaction, and the
rsyslog RFC 5424 handler with a bounded non-blocking queue. Admin screens for users,
roles, memberships and the audit trail.

**Done when:** the RBAC test matrix passes (every role × every action × in- and
out-of-scope), an unauthenticated request reaches no data, and mutations appear in
both the audit table and rsyslog. **All of this now works.**

Delivered beyond the original slice: scoped listing queries
([ADR-0016](adr/0016-scoped-listing-queries.md)), the per-person project rate
level, transparent Argon2id parameter upgrades on login, and session revocation
on every privilege change. TOTP and API tokens are designed for but deferred -
neither is load-bearing for a small team behind SSO, and both are additive.

## Layer 3 — Attachments and expenses

Content-addressed blob store with reference counting and an orphan sweep;
authorising download handler; upload validation (size, type allow-list, server-side
sniff, hostile-SVG handling); clipboard paste and drag-drop; image thumbnails.
Expenses with categories, billable/reimbursable flags, markup and receipts, included
in reports and exports.

**Done when:** a screenshot pasted into an entry survives a backup/restore cycle and
is not retrievable by a user who cannot see the owning record.

## Layer 4 — Proxy entries and approvals

Proxy proposals with `entered_by`, `pending` status and exclusion from all totals;
the inbox; accept / edit-and-accept / reject with reason; overlapping-proposal
flagging. Weekly timesheet submit → approve/reject → lock, with locked periods
rejecting edits.

**Done when:** proposed time is provably absent from every total and export until
accepted, and every transition is in the audit trail.

## Layer 5 — Reports, PDF and DOCX

Grouping by customer/project/assignment/person/tag over arbitrary ranges; budget
consumption and burn; billable vs non-billable; per-currency totals. Pure-Go PDF
with page breaks and repeating headers; OOXML DOCX writer. Golden-file tests for
all four formats, and the `client` projection asserted to omit internal data.

## Layer 6 — Semi-automatic assistance

Gap detection on the day view; idle detection with keep/discard/split; long-timer
review; end-of-day and end-of-week reminders; history-based suggestions ranked by
weekday. Built as a suggestion pipeline so calendar and git sources can be added
later without new machinery.

## Layer 7 — Hardening and packaging

Cross-platform CI (macOS, Linux, Windows × amd64/arm64); performance validation
against the 100k-entry dataset from ASR-012; backup/restore command and documented
restore drill; systemd unit and reverse-proxy examples; import from Toggl/Harvest
CSV; release artefacts with checksums.

---

## Backlog (not yet scheduled)

Calendar (ICS) import · git commit import · desktop activity agent · recurring
entries · invoice numbering · multi-currency conversion · internal cost rates and
margin reporting · fine-grained permissions · PostgreSQL backend · webhooks ·
public read-only client portal links · PWA/offline · Slack reminder integration.

## Out of scope

Invoicing and payment processing, payroll, project/task management, native mobile
apps. See [DESIGN.md](DESIGN.md) §11.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Overlapping-timer totals confuse users | they distrust the numbers | show summed *and* elapsed everywhere, explain in the UI, never auto-split ([ADR-0004](adr/0004-concurrent-timers.md)) |
| Pure-Go SQLite too slow at scale | ASR-012 missed | measure at 100k entries in Layer 1, index deliberately; PostgreSQL is a contained change ([ADR-0003](adr/0003-pure-go-sqlite.md)) |
| Bespoke DOCX writer produces files Word dislikes | broken deliverable | golden tests plus manual verification in Word *and* LibreOffice before Layer 5 ships |
| Proxy entries left unconfirmed | unbilled work | surface pending items in the header and reminders ([ADR-0005](adr/0005-proxy-time-entry.md)) |
| Windows path/locking differences | platform-specific bugs found late | Windows in CI from Layer 1, not Layer 7 |
| Seven themes drift as components are added | broken/illegible themes | one token set, automated contrast test ([ADR-0011](adr/0011-theming-via-css-custom-properties.md)) |
