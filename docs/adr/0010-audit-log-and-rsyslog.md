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
