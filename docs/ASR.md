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
| [ASR-015](#asr-015) | Security / Least privilege | The process runs with the least privilege each platform allows | A |
| [ASR-016](#asr-016) | Internationalisation | The interface is fully localisable, not merely translated | A |
| [ASR-017](#asr-017) | Learnability | Deliberate but surprising behaviour is explained in context | A |
| [ASR-018](#asr-018) | Recoverability | A user can take and restore their own backups, whole or partial | A |
| [ASR-019](#asr-019) | Interoperability | Existing hours can be brought in from a spreadsheet | A |

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

---

### ASR-015
**The process runs with the least privilege each platform allows.**

*Quality attribute:* Security / Least privilege

*Requirement:* A defect in the application must not give an attacker the machine.
The running process must be able to reach its own data and the few system paths
it needs, and nothing else, on all three supported platforms.

*Fit criterion:* The process holds no capabilities, cannot execute another
program, cannot write outside its data directory and temporary directory, and
cannot read another user's files. `scripts/harden-check.sh` reports the active
mechanisms for a running instance, and `systemd-analyze security timetracker`
scores the shipped unit. Where a platform offers no in-process mechanism, the
deployment configuration in `deploy/` supplies an equivalent and the application
reports honestly that nothing was applied in-process.

*Rationale:* Every other control in this system is application code, and
application code has defects. This is the layer that decides whether a defect
becomes an incident. It also constrains the design directly: the application
never spawns a subprocess, which is why PDF and DOCX generation had to be
in-process ([ADR-0007](adr/0007-pure-go-document-generation.md)), and it is why
document generation, SQLite and TLS all had to be dependency-free.

*Addressed by:* [ADR-0017](adr/0017-defence-in-depth-hardening.md), [ADR-0018](adr/0018-tls-termination.md)

---

### ASR-016
**The interface is fully localisable, not merely translated.**

*Quality attribute:* Internationalisation

*Requirement:* Every user-visible string, and every formatted number, duration,
amount and date, must follow the reader's language and its conventions. English
and Swedish ship; adding a third language must not require touching application
code.

*Fit criterion:* A parity test fails the build when any catalogue lacks a key
present in the default one. Every screen renders in every catalogued language
with no untranslated key reaching the page, asserted by scanning the rendered
output. Swedish renders `1 234,50` and `1 tim 30 min` where English renders
`1,234.50` and `1h 30m`. The document's `lang` attribute matches the language
actually rendered, in the first byte of the response.

*Rationale:* Translating words while still rendering `1.50` produces an
interface that reads as broken rather than foreign - and on a timesheet a
decimal point where a comma belongs is a different number, not a style choice.
Requiring the `lang` attribute to be correct from the first byte rules out
client-side translation, because a screen reader picks its voice from it.

*Addressed by:* [ADR-0019](adr/0019-message-catalogues-and-server-side-localisation.md)

---

### ASR-017
**Deliberate but surprising behaviour is explained in context.**

*Quality attribute:* Learnability / Usability

*Requirement:* Where the application does something defensible but unexpected -
overlapping timers, two disagreeing totals, a quick-add parser that refuses to
guess, rates frozen onto an entry - the explanation must be reachable from the
screen where the surprise happens.

*Fit criterion:* Every screen has help specific to it, reachable in one action
and in one keystroke. The help is translated by the same mechanism as the rest
of the interface. It works with JavaScript disabled, by navigating to a page.
Opening it moves focus into the panel and closing it returns focus to the
control that opened it.

*Rationale:* A manual in a repository does not help someone looking at two
different totals and wondering which is wrong. Requiring the help to work
without JavaScript and to manage focus is what stops it from being a decorative
panel that only some users can actually reach.

*Addressed by:* [ADR-0020](adr/0020-context-sensitive-help.md)

---

### ASR-018
**A user can take and restore their own backups, whole or partial.**

*Quality attribute:* Recoverability

*Requirement:* Someone must be able to take a copy of their data without an
administrator, keep it somewhere else, and put it back - in whole, or only the
part they need.

*Fit criterion:* A backup is a single file, produced in one action, covering
everything or narrowed to one customer, one project or one date range. Restoring
merges: restoring the same file twice creates nothing the second time, and
restoring an older backup does not remove newer work. A backup written by a
newer version is refused rather than partially read. Automatic backups run on an
interval when enabled, keep a bounded number, and a retention of zero removes
nothing.

*Rationale:* Backup features are used exactly once, under pressure, by someone
who has already lost something. Every property above exists so that the feature
cannot make that situation worse - which is why merge rather than replace is a
requirement and not an implementation detail.

*Addressed by:* [ADR-0021](adr/0021-json-backups-that-merge.md)

---

### ASR-019
**Existing hours can be brought in from a spreadsheet.**

*Quality attribute:* Interoperability / Suitability

*Requirement:* Hours already recorded elsewhere must be importable from CSV
without retyping, and without the user having to rearrange their file first.

*Fit criterion:* An import previews every row with its problems before writing
anything, and then imports every valid row or none. Column names are matched
across several common spellings. An ambiguous date format is reported rather than
guessed. Catalogue records are created only when explicitly requested, and the
preview lists exactly what would be created.

*Rationale:* Retyping a month of hours is what stops people adopting a tool at
all. The all-or-nothing rule is the requirement rather than a nicety: a partial
import leaves the user reconciling two sources, which is more work than the
retyping it replaced.

*Addressed by:* [ADR-0022](adr/0022-two-pass-csv-import.md)

---

### ASR-020
**Recorded hours can be declared finished, checked by someone else, and then
stop moving - without making a mistake permanent.**

*Quality attribute:* Integrity / Modifiability

*Requirement:* A person must be able to declare a week's hours finished; a
manager must be able to accept or return them; accepted hours must not change
afterwards; and a genuine error must still be correctable through a route that
leaves a record. A mistyped entry must be correctable at any time before the
week is declared finished, without deleting and re-entering it.

*Fit criterion:* Submitting a week refuses every subsequent change to time
inside it - creating, editing, moving, deleting, and starting or stopping a
timer - for every user including an administrator, with a message naming the way
to unlock it. Approval is refused to the week's owner whatever their role, and
to anyone without the manage permission. A rejection carries a reason, and that
reason is shown to the owner on the screen where they correct it. Reopening an
approved week is a distinct operation with its own audit record. Any entry can be
edited in place, including its date, start time and duration, and the edit is
audited with the previous value.

*Rationale:* Two failures are common in time tracking tools and both are
expensive. Hours that keep moving after they were invoiced mean the invoice
cannot be explained. Hours that can never be corrected mean people work around
the tool - a compensating entry in the wrong week, or a note in an email - and
the record silently stops describing reality. The requirement is to prevent the
first without causing the second, which is why "correctable through an audited
route" is part of the criterion rather than a convenience.

*Addressed by:* [ADR-0023](adr/0023-week-as-the-unit-of-approval.md)

---

### ASR-021
**A customer's contract terms decide what an hour and a claim are worth, and no
figure appears on an invoice that a person did not choose.**

*Quality attribute:* Suitability / Integrity

*Requirement:* Overtime, travel time and reimbursement must be expressible per
customer, because they are contract terms and every contract differs. Applying
them must not require a person to compute anything, and must not put a rate on
an entry that nobody decided to apply.

*Fit criterion:* Overtime and travel are recorded as a kind on the entry, chosen
by the person; no threshold reclassifies time automatically, and a threshold
that is exceeded produces a notice that stops once the time is marked. An
absolute rate takes precedence over a multiplier, and an unset rule bills at the
ordinary rate rather than at zero. Travel a customer does not pay for is
recorded in full and carries no amount. A distance or a number of days is priced
from the customer's rate, and the quantity, the unit and the rate used are all
stored on the claim. The kind appears in every export, so a line billed at other
than the base rate says why on the document carrying the figure. A claim above
the customer's evidence threshold is marked, and refuses the week's submission
until a receipt is attached.

*Rationale:* Both failure modes here are expensive and both are common. A tool
that will not express "time and a half over eight hours" makes people keep the
real numbers in a spreadsheet, so the timesheet stops being the record. A tool
that applies the multiplier automatically bills the ninth hour at a premium
because somebody forgot to stop a timer, and the resulting invoice cannot be
defended by the person who has to defend it. The requirement is to express the
terms without making the judgement.

*Addressed by:* [ADR-0024](adr/0024-customer-rate-rules.md)

---

### ASR-022
**Somebody who knows what they want to do, but not where to do it, can find out
from inside the application.**

*Quality attribute:* Learnability

*Requirement:* The normal actions must be documented as procedures, reachable
without knowing which screen performs them, translated, and available with no
network and no JavaScript.

*Fit criterion:* A task-oriented guide covers recording, correcting and moving
time, recording time for a colleague, submitting a week, approving one, claiming
expenses, exporting, backing up, and setting up customers and contract terms.
Each is numbered steps rather than prose. Every topic exists in every catalogued
language, enforced by a test. A topic that cannot apply in the running mode is
not offered. It is reachable from every screen, and an unknown topic renders the
guide rather than an error.

*Rationale:* Per-screen help answers "what am I looking at", which is a question
you can only ask once you are in the right place. "Put Erik's hours in before
the week closes" is the question people actually arrive with, and its answer
spans a screen, a syntax, a permission and a consent step that no single screen
explains. Documentation that only exists on a website fails the deployment this
application explicitly supports: a laptop with no network.

*Addressed by:* [ADR-0025](adr/0025-task-oriented-guide.md)
