# Architecturally Significant Requirements (ASR)

An ASR is a requirement that **changes the structure of the system** if it changes.
Ordinary features are tracked in [MVP_PLAN.md](MVP_PLAN.md); only requirements that
constrain the architecture live here.

Each ASR has:

| Field | Meaning |
|---|---|
| **ID** | Stable identifier, never reused. |
| **Quality attribute** | The ISO/IEC 25010-style category it belongs to. |
| **Requirement** | What must be true. |
| **Fit criterion** | An objective, testable statement. If you cannot test it, it is not a fit criterion. |
| **Rationale** | Why this matters enough to constrain the architecture. |
| **Addressed by** | The ADRs that realise it. |
| **Status** | `proposed` / `accepted` / `retired`. |

Status legend: **A** = accepted, **P** = proposed, **R** = retired.

## Register

| ID | Quality attribute | Requirement | Status |
|---|---|---|---|
| [ASR-001](#asr-001) | Usability / Suitability | Parallel tracking of multiple assignments | A |
| [ASR-002](#asr-002) | Portability | Runs on macOS, Linux and Windows | A |
| [ASR-003](#asr-003) | Deployability | Single self-contained binary, no runtime deps | A |
| [ASR-004](#asr-004) | Flexibility | One codebase serves local single-user and shared server use | A |
| [ASR-005](#asr-005) | Security | Authentication and authorisation in server mode | A |
| [ASR-006](#asr-006) | Auditability | Every mutating action is attributable and logged | A |
| [ASR-007](#asr-007) | Interoperability | Exports to PDF, CSV, DOCX and JSON | A |
| [ASR-008](#asr-008) | Integrity | Time attributed to a person requires that person's consent | A |
| [ASR-009](#asr-009) | Accessibility | Multiple themes incl. a high-contrast theme | A |
| [ASR-010](#asr-010) | Durability | User data survives upgrades and is recoverable | A |
| [ASR-011](#asr-011) | Maintainability | Code is readable by a human unfamiliar with it | A |
| [ASR-012](#asr-012) | Performance | Interactive operations feel instantaneous | A |
| [ASR-013](#asr-013) | Suitability | Rich capture: paste, attachments, expenses | A |
| [ASR-014](#asr-014) | Correctness | Money and duration arithmetic is exact | A |

---

### ASR-001
**Parallel tracking of multiple assignments.**

*Quality attribute:* Usability / Functional suitability

*Requirement:* A user must be able to record time against several assignments that
overlap in wall-clock time. On a typical day, work is split across ~5 assignments,
and some of that work genuinely happens concurrently (a build runs while a meeting
is in progress).

*Fit criterion:* Two or more timers can run simultaneously; the system stores and
reports overlapping intervals without data loss, and surfaces the overlap to the
user as information rather than as an error.

*Rationale:* This is the core differentiator from a naive "one clock" tracker. It
rules out any data model that assumes a single active entry per user, and it forces
reports to distinguish *elapsed* time from *summed* time.

*Addressed by:* [ADR-0004](adr/0004-concurrent-timers.md)

---

### ASR-002
**Runs on macOS, Linux and Windows.**

*Quality attribute:* Portability

*Requirement:* The application must build and run on all three desktop platforms on
both amd64 and arm64.

*Fit criterion:* `make build-all` cross-compiles from a single host without a C
toolchain, and the produced binaries start and serve the UI on a clean machine of
each target OS.

*Rationale:* Forbids cgo, which in turn rules out the most common SQLite driver and
any dependency on platform-specific system libraries. This single requirement drives
the choice of database driver, PDF generator and DOCX writer.

*Addressed by:* [ADR-0003](adr/0003-pure-go-sqlite.md), [ADR-0007](adr/0007-pure-go-document-generation.md)

---

### ASR-003
**Single self-contained binary, no runtime dependencies.**

*Quality attribute:* Deployability / Installability

*Requirement:* Running the application must not require installing a runtime, a
database server, a web server, or any Node.js toolchain.

*Fit criterion:* Copying `bin/timetracker` onto a machine with no development tools
and running it yields a working application. `ldd`-equivalent inspection shows no
non-system dynamic dependencies. No file outside the binary is required to serve the
UI, except the user's own data directory.

*Rationale:* The primary user is a consultant who wants to double-click a file, not
operate infrastructure. This forbids a separate frontend build artefact and forces
templates, CSS, JS and migrations to be embedded via `embed.FS`.

*Addressed by:* [ADR-0002](adr/0002-server-rendered-htmx.md), [ADR-0003](adr/0003-pure-go-sqlite.md), [ADR-0009](adr/0009-embedded-assets-and-migrations.md)

---

### ASR-004
**One codebase serves local single-user and shared server use.**

*Quality attribute:* Flexibility / Modifiability

*Requirement:* The same program must run as a private, no-login application on a
laptop and as an authenticated multi-user service on a server.

*Fit criterion:* A single binary invoked as `timetracker` (local) and
`timetracker --mode=server` (shared) exposes the same feature set, differing only in
the identity, authorisation and logging layers. No feature may be implemented twice.

*Rationale:* Two binaries would drift. This forces identity to be an injected
concern rather than something handlers assume, and it means every handler must ask
an authorisation layer rather than checking a global flag.

*Addressed by:* [ADR-0001](adr/0001-single-binary-two-modes.md), [ADR-0006](adr/0006-authentication-model.md)

---

### ASR-005
**Authentication and authorisation in server mode.**

*Quality attribute:* Security

*Requirement:* In server mode, no data is readable or writable without an
authenticated identity, and every access is checked against a role and a project
membership.

*Fit criterion:* An unauthenticated request to any route other than the login,
static-asset and health endpoints receives a redirect or 401, never data. A user
with the `member` role cannot read another user's time entries or any customer they
are not a member of; this is asserted by automated tests.

*Rationale:* Timesheets contain commercially sensitive data (who works on what, at
what rate, for whom). Adding authorisation late is how systems end up with checks in
the UI but not in the handlers.

*Addressed by:* [ADR-0006](adr/0006-authentication-model.md), [ADR-0008](adr/0008-rbac-model.md)

---

### ASR-006
**Every mutating action is attributable and logged.**

*Quality attribute:* Auditability / Non-repudiation

*Requirement:* Every create, update, delete, approval and export must record who did
it, when, to what, and from where. In server mode these events must also reach
rsyslog.

*Fit criterion:* For any row in the database, the audit log can answer "who last
changed this and when". Server-mode events appear in the configured syslog facility
in RFC 5424 format within one second of the action. Audit records are append-only:
there is no code path that updates or deletes them.

*Rationale:* Billable hours are the basis of invoices. When a client disputes an
invoice, the answer has to be evidence, not recollection. This forces a write path
that goes through a single auditable service layer rather than ad-hoc SQL in
handlers.

*Addressed by:* [ADR-0010](adr/0010-audit-log-and-rsyslog.md)

---

### ASR-007
**Exports to PDF, CSV, DOCX and JSON.**

*Quality attribute:* Interoperability

*Requirement:* Any report must be exportable in all four formats without external
tooling.

*Fit criterion:* Each format is produced by pure Go code with no subprocess and no
headless browser. Generated files open without warnings in Excel/LibreOffice Calc
(CSV), Word/LibreOffice Writer (DOCX) and a standards-compliant PDF viewer. JSON
validates against a documented schema.

*Rationale:* Invoicing and client reporting is the point of the exercise. Requiring
LaTeX, wkhtmltopdf or headless Chrome would violate ASR-003.

*Addressed by:* [ADR-0007](adr/0007-pure-go-document-generation.md)

---

### ASR-008
**Time attributed to a person requires that person's consent.**

*Quality attribute:* Integrity / Compliance

*Requirement:* A user may record time on behalf of a colleague, but such an entry
must not count as that colleague's hours until the colleague accepts it.

*Fit criterion:* A proxy-created entry is stored with status `pending`, an
`entered_by` distinct from `user_id`, and is excluded from all billing and reporting
totals for the subject until they accept it. Rejection is recorded, not silently
discarded.

*Rationale:* Recording hours in someone else's name without their agreement is a
labour-law and trust problem in most jurisdictions. Making consent structural rather
than procedural keeps it from being bypassed by a UI shortcut.

*Addressed by:* [ADR-0005](adr/0005-proxy-time-entry.md)

---

### ASR-009
**Multiple themes including a high-contrast theme.**

*Quality attribute:* Accessibility / Usability

*Requirement:* Light, dark, gold, sand, spring, autumn and high-contrast themes are
selectable, and the choice persists.

*Fit criterion:* Switching theme changes no HTML, only a `data-theme` attribute;
every theme is defined purely by CSS custom properties. The high-contrast theme meets
WCAG 2.1 AA contrast ratios (4.5:1 for body text, 3:1 for large text and UI borders),
verified by the contrast test in TEST.md.

*Rationale:* A theme system implemented with per-theme markup or per-theme
stylesheets becomes unmaintainable at seven themes. Requiring a single token set
constrains the CSS architecture from day one.

*Addressed by:* [ADR-0011](adr/0011-theming-via-css-custom-properties.md)

---

### ASR-010
**User data survives upgrades and is recoverable.**

*Quality attribute:* Durability / Recoverability

*Requirement:* Upgrading the binary must never lose or corrupt data, and a user must
be able to take a backup and restore it.

*Fit criterion:* Migrations are versioned, forward-only, applied automatically in a
transaction at startup, and recorded in a `schema_migrations` table. A binary refuses
to start against a database newer than it understands. Backup produces a single
consistent archive (database + attachments) that restores into a clean install.

*Rationale:* This is the user's timesheet: losing it means losing invoiceable money.

*Addressed by:* [ADR-0009](adr/0009-embedded-assets-and-migrations.md)

---

### ASR-011
**Code is readable by a human unfamiliar with it.**

*Quality attribute:* Maintainability

*Requirement:* The source must be commented and structured so a competent Go
developer can understand *why* code exists, not just what it does.

*Fit criterion:* Every exported symbol has a doc comment. Every package has a
package comment stating its responsibility and its boundaries. Non-obvious decisions
carry an inline comment referencing the ADR that explains them. `go vet` and
`gofmt -l` are clean.

*Rationale:* An explicit project goal. It also constrains design: clever code that
needs a paragraph to explain is a signal to choose the duller alternative.

*Addressed by:* [ADR-0012](adr/0012-layered-package-structure.md)

---

### ASR-012
**Interactive operations feel instantaneous.**

*Quality attribute:* Performance efficiency

*Requirement:* Starting/stopping a timer and navigating the timesheet must not feel
laggy, on a laptop, for a realistic multi-year dataset.

*Fit criterion:* With 100,000 time entries, server response time for the day view,
week view and timer start/stop is under 100 ms at the 95th percentile on commodity
hardware. Report generation over a one-year range completes in under 2 s.

*Rationale:* Sets the bar for indexing strategy and rules out loading whole tables
into memory. It also validates the server-rendered choice: a page render must be
cheap enough to be the response to a button press.

*Addressed by:* [ADR-0002](adr/0002-server-rendered-htmx.md), [ADR-0003](adr/0003-pure-go-sqlite.md)

---

### ASR-013
**Rich capture: paste, attachments and expenses.**

*Quality attribute:* Functional suitability

*Requirement:* Users must be able to paste text and images directly into entries,
attach files and photos, and record billable and reimbursable expenses with receipts.

*Fit criterion:* An image on the clipboard can be pasted into a time entry or expense
and is stored as an attachment without leaving the page. Attachments are retrievable
only by users authorised to see the owning record. Billable and reimbursable expenses
are distinguishable in every report and export.

*Rationale:* Binary data changes the storage story: the database is no longer the
only durable store, so backup, authorisation and deduplication all have to account
for a blob store.

*Addressed by:* [ADR-0013](adr/0013-attachment-storage.md)

---

### ASR-014
**Money and duration arithmetic is exact.**

*Quality attribute:* Correctness

*Requirement:* No rounding drift may appear in invoiced amounts or reported
durations.

*Fit criterion:* Money is stored and computed in integer minor units (cents) with an
explicit currency; durations are stored in whole seconds. No `float64` appears in any
persisted field or in any total. Rounding rules (e.g. round each entry up to 15
minutes) are applied at explicitly documented points and are covered by tests
including boundary values.

*Rationale:* Floating-point money is the classic defect that surfaces only after an
invoice is disputed. Making it a structural rule is cheaper than finding it later.

*Addressed by:* [ADR-0014](adr/0014-exact-money-and-duration.md)
