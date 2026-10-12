# ADR-0010: Append-only audit log, rsyslog via RFC 5424

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-006, ASR-005, ASR-008

## Context

Timesheets are financial records. Two distinct needs follow, and they are commonly
conflated:

* **Audit** — an answer to "who changed this entry from 2 hours to 5, and when?",
  queryable from inside the application, retained as long as the data it describes.
* **Operational logging** — what the process is doing, for diagnosis, shipped to
  wherever the operator collects logs. The requirement names **rsyslog**.

If audit records live only in the operational log they are unqueryable and get
rotated away. If operational events live only in the database they are invisible to
the ops tooling, and a compromised instance can rewrite them.

## Decision

**Two sinks, one call site.**

Every mutation goes through the service layer (ADR-0012), which writes an
`audit_events` row **in the same transaction as the change itself**. If the audit
write fails, the change fails. The row records: actor, effective identity if acting
by proxy, action, resource type and id, a compact diff of changed fields, timestamp,
source address, request id, and session id. The table is append-only by
construction: no service method updates or deletes it, and the schema documents it
as an invariant.

The same event is emitted to the **operational log** as structured `log/slog`
output. In server mode a second handler forwards to **rsyslog** in **RFC 5424**
format, over unix socket (`/dev/log`) or TCP/TLS to a remote collector, with
configurable facility and a bounded, non-blocking buffer — a syslog collector that
goes away must never block a user's request or fail their write. Delivery is
best-effort by design; the database audit trail is the record of truth, and the
number of dropped syslog messages is itself logged and exposed on the health
endpoint.

Authentication events (success, failure, lockout, session creation and revocation,
role change) are logged in both sinks regardless of whether they mutate domain data.

**Secrets, password hashes, session and API tokens are never logged**, in either
sink. Attachment contents are never logged; their metadata is. This is enforced by a
redaction step in the logging package rather than by discipline at call sites, and
tested.

## Consequences

**Positive**

* A disputed invoice is answerable from within the application, with a trail that
  cannot have been quietly edited by the app itself.
* Atomicity means there is no window in which a change exists without its audit
  record.
* Operators get events in the format their existing infrastructure already parses,
  without the app depending on that infrastructure being up.

**Negative / accepted costs**

* Roughly one extra row written per mutation: the audit table will become the
  largest in the database. Retention and export-then-prune is a documented
  operational task, not an automatic deletion.
* Coupling audit to the transaction means a slow audit write slows the user's
  request. The write is a single insert; acceptable.
* Best-effort syslog means an operator can lose events during a collector outage.
  Made explicit rather than hidden, and the in-database trail is unaffected.
* Append-only is a discipline enforced by code review and tests, not by SQLite —
  anyone with the database file can edit it. True immutability would need hash
  chaining or an external log; noted as a future ADR if a client ever requires it.
* Atomicity has to be arranged at each call site, and for a long time it was not.
  Only the time-entry paths shared a transaction; every catalogue mutation wrote
  the record on one connection and then opened a second transaction for the audit
  row, so a failed audit reported failure to the user over a change that had
  already committed. Nothing failed while nothing went wrong, which is why it
  survived until a test injected a failure into the audit insert. The catalogue
  now goes through `Service.mutate`, which takes the change as a function of the
  transaction.

  The importers and attachments followed, each needing an answer of its own. A
  CSV import prepares every row and then writes them all, with their audit rows
  and the summary, in one transaction ([ADR-0022](0022-two-pass-csv-import.md)).
  A calendar import does the same while keeping its per-meeting refusals, which
  happen during preparation, before anything is written. An attachment commits
  its row and audit entry together, with the bytes written before the
  transaction and removed after it ([ADR-0013](0013-attachment-storage.md)).

  The account and timesheet paths followed, and two of them carry a second write
  that has to go with the first: changing a role signs the account out, and
  changing a password signs it out everywhere. A partial commit there leaves an
  account whose privileges and whose sessions disagree, so the sign-out is inside
  the transaction with the change and the audit row. A tag rename is the same
  shape for a different reason — it rebuilds the search index, which has to
  happen inside the same transaction or the rebuild indexes the old name.

  **Every mutating path in the service now works this way**, and there is one
  stated exception: the summary row at the end of a restore. A restore is not a
  change but a sequence of them — every record it creates commits with its own
  audit row — and it merges by name, so it is safe to run again. There is no
  transaction that summary could join without holding the single write
  connection for the length of a restore of somebody's whole history, so its
  failure is logged and the restore stands. Reporting a failed restore to
  somebody whose data is restored and fully recorded is the worse of the two
  available lies.

  The injection tests in `internal/service/audit_test.go` are what settle this
  for any given path, and a path with no case there has not been checked. That
  is how each of these groups was found in turn: the tests came first, the list
  of what was still wrong came from them, and it shrank as they were fixed.

## Alternatives considered

**Audit via database triggers** — impossible to bypass from application code.
Rejected: triggers cannot see the actor, the request context or the intent, and
SQLite trigger logic is hard to test and to migrate.

**Operational log only, parsed later** — one sink. Rejected on ASR-006: unqueryable
from the app, rotated away, and dependent on log shipping being healthy at the
moment something interesting happened.

**Event sourcing as the primary model** — the audit trail becomes the data. A clean
answer to non-repudiation, rejected as disproportionate: it reshapes the entire
persistence design of a timesheet app, badly against ASR-011.

## Related

* ADR-0005 (proxy entries), ADR-0012 (service boundary)
