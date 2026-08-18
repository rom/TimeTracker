# Architecture

> **Scope.** This document describes *how the system is built*: its structure,
> boundaries, data model and runtime behaviour. For *why* each significant choice
> was made, see the [ADRs](adr/README.md); for the requirements that constrain the
> architecture, see [ASR.md](ASR.md); for the user-facing behaviour and interaction
> design, see [DESIGN.md](DESIGN.md).

## 1. Overview

TimeTracker is a single-binary Go web application for tracking billable time and
expenses across customers, projects and assignments. It runs in one of two modes
from the same executable ([ADR-0001](adr/0001-single-binary-two-modes.md)):

| | **Local mode** | **Server mode** |
|---|---|---|
| Users | one (the person who started it) | many |
| Auth | none | local accounts + OIDC ([ADR-0006](adr/0006-authentication-model.md)) |
| Authorisation | permissive authoriser | RBAC + project membership ([ADR-0008](adr/0008-rbac-model.md)) |
| Bind | `127.0.0.1`, random-ish port, may open a browser | explicit address, TLS terminated in front |
| Logging | stderr | stderr + rsyslog ([ADR-0010](adr/0010-audit-log-and-rsyslog.md)) |
| Storage | `~/…/timetracker/timetracker.db` + blobs | operator-chosen data directory |

Everything else — features, screens, data model, exports — is identical.

## 2. Context

```
   ┌────────────┐   HTTPS/HTTP    ┌─────────────────────────────┐
   │  Browser   │ ───────────────▶│      timetracker binary     │
   │ (HTMX + JS)│ ◀─────────────── │  HTTP · services · storage │
   └────────────┘   HTML fragments └──────────┬──────────┬───────┘
                                              │          │
                                    ┌─────────▼───┐  ┌───▼──────────┐
                                    │ SQLite file │  │ blob directory│
                                    └─────────────┘  └───────────────┘
                                              │
                  server mode only:  ┌────────▼────────┐   ┌──────────────┐
                                     │    rsyslog      │   │ OIDC provider │
                                     │ (socket or TCP) │   │  (optional)   │
                                     └─────────────────┘   └──────────────┘
```

External dependencies are all optional and all in server mode: an rsyslog collector
and an OIDC provider. Nothing else is required at runtime
([ADR-0003](adr/0003-pure-go-sqlite.md), [ADR-0007](adr/0007-pure-go-document-generation.md)).

## 3. Layers

Four layers, strictly one-directional
([ADR-0012](adr/0012-layered-package-structure.md)):

```
cmd/timetracker/          wiring only: flags, config, mode selection, lifecycle
│
├── internal/web/         HTTP. Routing, middleware, form decoding, templates,
│   │                     HTMX fragment rendering. Makes NO authorisation
│   │                     decisions and issues NO SQL.
│   ├── templates/        html/template files (embedded)
│   └── static/           CSS, JS, fonts, icons (embedded)
│
├── internal/service/     THE enforcement point. Authorisation, transactions,
│                         audit writes, business rules, orchestration.
│
├── internal/store/       SQL, migrations, row↔domain mapping. Knows nothing
│   └── migrations/       about users, HTTP or permissions.
│
└── internal/domain/      Types and invariants. No I/O, no imports of the above.
```

Cross-cutting, imported by any layer, importing none of them:

| Package | Responsibility |
|---|---|
| `internal/config` | flags, environment, config file, defaults, validation |
| `internal/logging` | `slog` setup, rsyslog handler, redaction |
| `internal/auth` | identity resolution, sessions, password hashing, OIDC, `Can()` |
| `internal/export` | CSV, JSON, PDF, DOCX writers over one `Report` value |
| `internal/blob` | content-addressed attachment store |
| `internal/timeutil` | UTC/local conversion, week boundaries, rounding rules |

**The rules that make the layering load-bearing** — these are not style
preferences, they are what makes ASR-005 and ASR-006 provable:

1. `internal/web` does not import `internal/store`. Enforced by an architecture
   test (see [TEST.md](TEST.md)).
2. Transactions begin and end in `internal/service`. A mutation and its audit row
   commit together or not at all.
3. The actor is resolved once, by middleware, into `context.Context`. Service
   methods read it from there and ask `auth.Can()`. No service method takes a
   "current user" argument a caller could forge, and none inspects the run mode.
4. `internal/domain` compiles with no dependency on anything in this list.

## 4. Request lifecycle

A representative mutating request — stopping a timer:

```
POST /timers/42/stop            (HTMX, from the running-timer header)
  │
  ├─ recovery ────────────── panics become 500 + a logged stack, never a crash
  ├─ request id ──────────── generated, in ctx, in every log line and audit row
  ├─ real IP ─────────────── trusted-proxy aware; recorded in the audit trail
  ├─ logging ─────────────── method, path, status, duration, actor, request id
  ├─ security headers ────── CSP (no inline script), nosniff, frame-ancestors none
  ├─ session ─────────────── cookie → session record → User → ctx   [server mode]
  ├─ CSRF ────────────────── token checked for every unsafe method  [server mode]
  ├─ rate limit ──────────── per-actor and per-IP on sensitive routes
  └─ route → handler
        │  decodes the form, no business logic
        ▼
     service.StopTimer(ctx, entryID)
        ├─ auth.Can(ctx, ActionUpdate, entry) ......... else ErrForbidden
        ├─ BEGIN
        ├─ store.GetTimeEntryForUpdate(tx, id)
        ├─ domain rules: already stopped? in a locked/approved period?
        ├─ store.UpdateTimeEntry(tx, …)
        ├─ store.InsertAuditEvent(tx, actor, "time_entry.stop", diff, …)
        └─ COMMIT
        └─ logging.Audit(event)  → stderr + rsyslog (best effort, non-blocking)
        ▼
     handler renders the `running-timers` + `day-totals` fragments
        │
        ▼
     200 OK, two HTML fragments, swapped in place by HTMX
```

Failure semantics: an authorisation failure renders the same "not found" shape as a
missing record for resources the actor may not even know exist; a domain rule
violation renders an inline error in the fragment; an infrastructure failure rolls
back the transaction, so no change exists without its audit row.

## 5. Data model

Domain shape, as maintained by the migrations in `internal/store/migrations`.
Concrete column choices follow [ADR-0014](adr/0014-exact-money-and-duration.md)
(integer minor units, whole seconds) and
[ADR-0015](adr/0015-utc-storage-local-display.md) (UTC ISO-8601 strings, IANA zone
names).

```
customer ──< project ──< assignment ──< time_entry >── user
    │           │             │              │
    │           │             │              ├──< attachment
    │           │             │              └──> (entered_by → user)
    │           └──< project_member >── user
    └──< expense >── user ──< session
                       │
                       └──< api_token

tag >──< time_entry (many-to-many)          audit_event (append-only)
rate (customer|project|person-on-project|entry override)
timesheet_period (one row per user per week; absent ⇒ open)
schema_migrations
```

Key entities:

| Table | Notable columns | Notes |
|---|---|---|
| `customer` | name, code, currency, colour_key, icon, archived_at | archive, never delete — history must stay readable |
| `project` | customer_id, name, code, colour_key, icon, budget_minutes, budget_amount, billable_default, rounding_rule | |
| `assignment` | project_id, name, code, colour_key, icon, billable_default, rate_override, sort_order, archived_at | the thing a timer runs against |
| `time_entry` | user_id, entered_by, assignment_id, started_at, ended_at (nullable ⇒ running), duration_seconds (derived, stored), note, billable, status, tz, rounding_rule_applied, billable_seconds, rate_minor, amount_minor, currency, approved_by, approved_at, locked | no unique constraint on "running per user" ([ADR-0004](adr/0004-concurrent-timers.md)) |
| `expense` | user_id, project_id, date, category, description, amount_minor, currency, kind (`billable`\|`reimbursable`\|`both`), markup_pct, status, receipt attachment | |
| `attachment` | owner_type, owner_id, sha256, filename, mime, size, uploaded_by | bytes on disk ([ADR-0013](adr/0013-attachment-storage.md)) |
| `user` | display_name, email, role, password_hash (nullable), oidc_subject (nullable), totp_secret (nullable), tz, theme, active | |
| `project_member` | project_id, user_id, role_override | the scoping dimension of RBAC |
| `timesheet_period` | user_id, week_start, status, submitted_at, submitted_seconds, decided_by, decided_at, note | one person's week; a missing row means open ([ADR-0023](adr/0023-week-as-the-unit-of-approval.md)) |
| `audit_event` | actor_id, on_behalf_of, action, resource_type, resource_id, diff_json, at, ip, request_id | append-only ([ADR-0010](adr/0010-audit-log-and-rsyslog.md)) |

**Indexing** is driven by ASR-012. The load-bearing ones:
`time_entry(user_id, started_at)` for the day and week views,
`time_entry(assignment_id, started_at)` for project reports,
a partial index on `time_entry(user_id) WHERE ended_at IS NULL` for the
running-timer header (queried on every page render),
`time_entry(status)` for the proxy inbox, and `audit_event(resource_type,
resource_id, at)` for record history.

**Deletion policy.** Customers, projects and assignments are archived, never
deleted, because deleting them would orphan invoiced history. Time entries can be
deleted by their owner while unapproved; the deletion is an audit event carrying the
prior state.

**Period locking.** Every mutation of a time entry passes through one function,
`service.checkPeriodOpen`, before it writes: create, update, delete, start,
stop and move. That is deliberate rather than incidental - a new mutation either
calls it or the lock does not cover that path, which is a property one `grep`
can check. An update consults **both** weeks when the date changes, since moving
an entry out of an open week into a submitted one alters the submitted one.
The check is not conditional on role: reopening
([ADR-0023](adr/0023-week-as-the-unit-of-approval.md)) is the only way through,
and it leaves a record.

## 6. Concurrency and transactions

* SQLite in WAL mode: one writer, many concurrent readers
  ([ADR-0003](adr/0003-pure-go-sqlite.md)). Writes go through a single connection;
  reads use a pool. `busy_timeout` covers the rest.
* Transactions are short and never span an HTTP round trip or an external call.
* Overlapping timers mean concurrent writes to different rows, not the same row;
  the one genuine race — two requests stopping the same timer — is handled with a
  conditional update (`WHERE ended_at IS NULL`) and a re-read, not a lock.
* Background work is minimal and each piece runs as one goroutine with a context
  cancelled at shutdown: the idle/long-timer sweep, the session pruner, the orphan
  blob sweep, and the syslog forwarder's bounded queue.
* Shutdown is graceful: stop accepting, drain in-flight requests with a timeout,
  close the syslog queue, checkpoint WAL, close the database.

## 7. Configuration

Precedence, lowest to highest: built-in defaults → config file → environment
(`TT_…`) → command-line flags. Configuration is validated once at startup and the
process refuses to start on an invalid combination rather than degrading — a server
without a session secret, or bound to a non-loopback address with no TLS in front
and no explicit acknowledgement, is a configuration error, not a warning.

Data directory defaults per platform (ASR-002): `~/Library/Application
Support/TimeTracker` on macOS, `$XDG_DATA_HOME/timetracker` or
`~/.local/share/timetracker` on Linux, `%LOCALAPPDATA%\TimeTracker` on Windows.

## 8. Build and release

`make build` produces `bin/timetracker` for the host; `make build-all`
cross-compiles darwin/linux/windows × amd64/arm64 from any host, because nothing
uses cgo ([ADR-0003](adr/0003-pure-go-sqlite.md)). Version, commit and build date
are stamped in via `-ldflags`, reported by `timetracker version` and on the health
endpoint. Templates, CSS, JS, fonts and migrations are embedded
([ADR-0009](adr/0009-embedded-assets-and-migrations.md)); `make dev` builds with the
`dev` tag so assets load from disk and templates re-parse per request.

## 9. Deployment

**Local.** Run the binary. It creates its data directory, migrates, binds loopback
and opens a browser.

**Server.** Run behind a reverse proxy that terminates TLS (nginx, Caddy, Traefik).
The application trusts `X-Forwarded-For` only from configured proxy addresses.
Recommended: a dedicated unprivileged system user, a systemd unit with
`ProtectSystem=strict` and a writable path for the data directory only, and
`StandardOutput=journal` alongside the rsyslog forwarding. Backups: the built-in
archive (database snapshot + blobs) on a schedule; restore is documented and tested.

## 10. Quality attribute scenarios

How the architecture answers the ASRs, and where it is weak:

| ASR | Mechanism | Residual risk |
|---|---|---|
| 001 parallel work | no uniqueness constraint on running entries; dual totals | users leave timers running — mitigated by the persistent header and the long-timer sweep |
| 002 three platforms | pure Go, no cgo, embedded tzdata | Windows path and file-locking behaviour needs real CI coverage |
| 003 single binary | `embed.FS`, in-process document generation | binary size grows with embedded fonts |
| 005 authz | one `Can()`, service-layer enforcement, architecture test | a new service method could forget the check — covered by a per-method test list |
| 006 audit | audit row in the same transaction | append-only is a code discipline, not a storage guarantee |
| 012 performance | targeted indexes, fragment rendering, WAL | report queries over multi-year ranges need measurement, not assumption |
| 014 exactness | integer minor units, one rounding point, rule recorded per entry | multi-currency totals are per-currency, never converted |

## 11. Known limitations

* Single-node only. Two instances against one SQLite file is unsupported and
  unsafe; horizontal scaling would require [ADR-0003](adr/0003-pure-go-sqlite.md) to
  be superseded by PostgreSQL.
* Write throughput is bounded by SQLite's single writer — appropriate for a team,
  not for hundreds of concurrent users.
* No offline capability; the browser must reach the server.
* Automatic activity tracking is limited to what a web page can observe
  ([DESIGN.md](DESIGN.md) §semi-automatic); anything deeper requires a desktop agent
  that does not yet exist.

## 12. Implementation status

What is described above is the target architecture. As of the current release:

| Area | State |
|---|---|
| Layers, service boundary, audit-in-transaction | implemented, enforced by an architecture test |
| Local mode | implemented |
| Server mode (auth, RBAC, sessions, rsyslog) | implemented; TOTP and API tokens deferred |
| Concurrent timers, dual totals, gap detection | implemented |
| Billing: layered rates, rounding, per-entry snapshot | implemented, including the person-on-project level and inheritance from the customer |
| CSV, JSON, PDF and DOCX export | implemented; the PDF cross-reference table is verified by an independent parse in the tests |
| Attachments, expenses, proxy entries | implemented |
| Weekly submit, approve, reject, reopen; period locking | implemented |
| Editing a recorded entry | implemented, audited with the previous value |
| Bulk CSV import, backup and restore, YAML configuration | implemented |
| i18n (English, Swedish), a11y, context-sensitive help | implemented |
| Tags | designed, not implemented |
| Client-side enhancement | `static/js/app.js` implements the HTMX subset in use (`hx-post`, `hx-confirm`, `HX-Request`/`HX-Refresh`); the upstream library is a drop-in replacement requiring no template changes |

See [MVP_PLAN.md](MVP_PLAN.md) for the sequence.
