# Security

> **Scope.** The threat model, the controls, and their limits. Design rationale for
> the security-relevant decisions lives in
> [ADR-0006](adr/0006-authentication-model.md),
> [ADR-0008](adr/0008-rbac-model.md),
> [ADR-0010](adr/0010-audit-log-and-rsyslog.md) and
> [ADR-0013](adr/0013-attachment-storage.md).

## 1. What we are protecting

Timesheets are commercially sensitive: who works for whom, at what rate, on what.
Concretely, the assets are billing data (hours, rates, amounts), client
identities and engagement details, attachments (receipts, screenshots — often
containing more than intended), credentials and session material, and the audit
trail whose value depends entirely on its integrity.

## 2. Threat model

**Local mode.** The trust boundary is the machine. The process binds loopback only
and serves the user who started it. An attacker with access to that account has the
data regardless of what the application does; we do not pretend otherwise. What we
*do* defend against: other applications on the machine reaching the port (loopback
binding, and rejection of unexpected `Host` headers to blunt DNS rebinding), and a
browser page from another origin driving the app (CSRF protections and a strict
CSP are active in both modes).

**Server mode.** The trust boundary is the network perimeter and the authentication
layer. Adversaries considered:

| Adversary | Concern |
|---|---|
| Unauthenticated network attacker | reaching any data; brute-forcing accounts; exploiting the upload path |
| Authenticated user, wrong scope | reading a customer or colleague they have no membership for; escalating role |
| `client` role user | seeing internal notes, cost data, or other customers |
| Malicious upload | stored XSS via an attachment, path traversal, resource exhaustion |
| Compromised or hostile OIDC response | forged identity, token replay, account takeover by email reassignment |
| Insider disputing records | altering history without trace |

**Explicitly out of scope:** a compromised host or database file (anyone with the
file has the data — encryption at rest is the operator's job, via disk encryption),
a malicious administrator, denial of service at the network layer, and the security
of the reverse proxy terminating TLS.

## 3. Controls

### Authentication ([ADR-0006](adr/0006-authentication-model.md))

* **Argon2id** password hashing, per-user salt, OWASP-current parameters stored
  alongside the hash so they can be raised and the hash upgraded on next login.
* Constant-time verification and a **uniform failure response**, so the endpoint
  does not disclose which accounts exist.
* **Rate limiting and lockout** per account and per source address, with a delay
  that grows on repeated failure. Lockouts are audited.
* **OIDC Authorization Code + PKCE**; `state` and `nonce` verified; ID token
  signature, issuer, audience and expiry validated; accounts linked by the immutable
  `sub` claim — **never by email**, which can be reassigned in a directory.
* **Optional TOTP** second factor, with single-use recovery codes.
* **API tokens** stored hashed, scoped, individually revocable, and never logged.

### Sessions

Opaque high-entropy identifiers referencing **server-side** session records — so
revocation is immediate, which is what matters when someone leaves. Cookies are
`HttpOnly`, `Secure`, `SameSite=Lax`, host-only. The session identifier is
**rotated on login and on any privilege change** to defeat fixation. Both idle and
absolute lifetimes are enforced, expired sessions are pruned, and a user can list
and revoke their own active sessions.

### Authorisation ([ADR-0008](adr/0008-rbac-model.md))

Four roles scoped by project membership, enforced at **one** point in the service
layer. The UI hides what a user cannot do, but hiding is presentation and never
enforcement — every action is re-checked server-side, and a test asserts that every
service method consults `Can()`. Defaults are least privilege: a new or
SSO-provisioned user is a `member` with no memberships and therefore sees nothing.
The `client` role receives a **narrowed projection** built in the service layer —
internal notes, cost data and colleague identities are removed before the data
leaves it, so no template bug can leak them. Resources a user may not know exist
return "not found" rather than "forbidden".

### Web

* **CSRF**: a token bound to the session required for every unsafe method, plus
  `SameSite=Lax` as defence in depth.
* **CSP** with no `unsafe-inline` for scripts; all JavaScript is served as embedded
  files, never inlined.
* `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`,
  `frame-ancestors 'none'`, and HSTS in server mode.
* **Contextual output escaping** via `html/template`; HTMX fragments go through the
  same escaping. `template.HTML` is never constructed from user input.
* **Parameterised SQL only.** No string-built queries anywhere; enforced by review
  and by the store package being the only place SQL exists.
* Request body and upload size limits, and read/write/idle timeouts on the server.

### Attachments ([ADR-0013](adr/0013-attachment-storage.md))

Content-addressed storage means the client's filename never becomes a path — path
traversal is structurally impossible. Every download goes through an **authorising
handler**; the blob directory is never a static route. Uploads are size-capped and
type-restricted, with a **server-side content sniff that must agree with the
extension** (the client's claimed type is not trusted). Responses set a
server-determined `Content-Type`, `nosniff`, and `Content-Disposition: attachment`.
SVG is treated as hostile — it is script-capable — and is refused or rasterised
rather than served inline.

### Audit and logging ([ADR-0010](adr/0010-audit-log-and-rsyslog.md))

Every mutation writes an audit row **in the same transaction** as the change, so no
change can exist without its record. Authentication events (success, failure,
lockout, session and role changes) are always logged. Server mode forwards to
rsyslog in RFC 5424 over unix socket or TCP/TLS, through a **bounded, non-blocking**
queue — a dead collector must never block or fail a user's request; drops are
counted and surfaced. **Secrets, password hashes, session identifiers, API tokens
and attachment contents are never logged**, enforced by a redaction step in the
logging package rather than by discipline at call sites, and tested.

### Dependencies and build

The dependency list is deliberately tiny — the standard library, a pure-Go SQLite
driver, and small vendored front-end assets — because every dependency is
attack surface. `govulncheck` runs in CI, dependencies are pinned by `go.sum`, and
`CGO_ENABLED=0` removes an entire class of memory-safety concerns. Release binaries
are published with checksums.

## 4. Operator responsibilities

The application cannot do these for you:

* **Terminate TLS** in front of server mode. The app refuses to bind a non-loopback
  address without either TLS or an explicit acknowledgement, but it does not
  validate your proxy's configuration.
* **Encrypt the disk.** The database and blobs are not encrypted at rest.
* **Restrict file permissions** on the data directory (`0700`) and run as a
  dedicated unprivileged user.
* **Back up, and rehearse a restore.** An untested backup is not a backup.
* **Configure the trusted proxy addresses**, or `X-Forwarded-For` is attacker
  controlled and your audit trail records fiction.
* **Set a strong session secret** and keep it out of version control. The server
  refuses to start without one.
* **Rotate and retain logs**, and protect the syslog transport (use TLS off-host).

## 5. Known limitations

Stated plainly, because a security document that only lists strengths is not useful:

* The audit table is append-only **by code discipline**, not by storage guarantee.
  Anyone with the database file can edit it. Tamper-evidence would need hash
  chaining or an external log — a future ADR if a client requires it.
* Data is unencrypted at rest.
* rsyslog delivery is best-effort; the in-database trail is the record of truth.
* No per-field encryption, so a database read is a full compromise of the data.
* Rate limiting is in-process; it does not survive a restart and does not coordinate
  across instances (there is only one instance by design).
* Attachment scanning is type and size validation only — there is **no malware
  scanning**. Files are served as downloads, never rendered inline.
* Local mode has no authentication by design; anyone with the user's account has the
  data.

## 6. Reporting a vulnerability

Report privately to the maintainer rather than in a public issue, with steps to
reproduce and the affected version. Expect acknowledgement within a few days.
Please do not test against an instance you do not own.
