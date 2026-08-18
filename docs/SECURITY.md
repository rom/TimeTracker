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

### Approval and period locking ([ADR-0023](adr/0023-week-as-the-unit-of-approval.md))

Submitted and approved weeks refuse every mutation of the time inside them, and
the check is one function that every mutating path calls — create, update,
delete, timer start, timer stop, and moving entries between assignments. **The
lock is not conditional on role.** An administrator is refused like anyone else;
reopening is the only way through and it writes an audit record naming who did
it, when, and why.

Nobody decides on their own timesheet, whatever their role, and the check is on
the identity in the request rather than on which control the page offered. An
edit that changes an entry's date is checked against **both** weeks, so the lock
cannot be walked around by moving time out of a locked week into an open one.

The total as it stood at submission is stored with the decision, so an approval
records the figures that were actually approved. If the current total ever
diverges — a restored backup can do it — the queue flags the week rather than
presenting the new number as though it had been submitted.

### Documentation and help content

Help and guide text lives in the message catalogues and is rendered through one
restricted markup subset: paragraphs, lists, bold and code, and nothing else.
The text is HTML-escaped **before** any tag of ours is inserted, in list items as
much as in paragraphs, and that ordering is what the tests pin. It matters
because catalogue content is written by translators rather than by the
application, and its output is marked as trusted HTML for the template.

### Search and user-supplied patterns

Regular-expression search compiles the user's pattern with Go's `regexp`, which
is RE2: linear time, no backtracking. A pathological pattern therefore cannot
hang the process the way it could with a PCRE engine, which is most of why
exposing patterns to users is defensible at all. Patterns are compiled and
rejected before the query is built, so a malformed one is a message rather than
a database error. Everything else typed into the search box is matched
literally: the full-text query is quoted so a user's `AND`, `*` or quotation
mark is searched for rather than executed as query syntax.

### Web

* **CSRF**: a token bound to the session required for every unsafe method, plus
  `SameSite=Lax` as defence in depth.
* **CSP** with no `unsafe-inline` at all - not for scripts, and not for styles.
  All JavaScript and CSS is served as embedded files. The style half is the one
  that costs something: the day timeline needs per-block geometry, which is
  naturally an inline `style` attribute and is refused. Rather than relax the
  policy for it, positions are expressed as CSS grid classes the stylesheet
  generates and the server chooses from. Allowing inline styles for geometry
  would allow them for everything, and a policy with an exception in it is a
  policy somebody will widen next year.
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

### Transport

The application can terminate TLS itself (`--tls-cert`, `--tls-key`) or sit
behind a proxy that does. TLS 1.2 is the floor, and the 1.2 suites are restricted
to AEAD constructions with forward secrecy - no CBC (padding-oracle attacks) and
no RSA key exchange (a stolen key would decrypt captured traffic retroactively).
The private key is refused if it is group- or world-readable on a POSIX system.

**Server mode refuses to bind a non-loopback address** without either its own
certificate, `--secure-cookies` declaring that TLS terminates upstream, or an
explicit `--allow-insecure`. HSTS is available but off by default: it is one of
the few headers that is genuinely hard to undo, and a misconfigured deployment
can be unreachable in a browser for months. See
[ADR-0018](adr/0018-tls-termination.md) and `scripts/gen-cert.sh`.

### Process hardening

Every control above is application code, and application code has defects. What
limits the damage is the operating system, and each platform's mechanism is
shipped in [`deploy/`](../deploy/README.md):

| Platform | Mechanism |
|---|---|
| Linux | **Landlock** applied by the process itself (`--hardening`), plus a systemd unit with a seccomp filter, an empty capability bounding set, `ProtectSystem=strict`, `MemoryDenyWriteExecute` and restricted address families |
| Debian/Ubuntu | an **AppArmor** profile, enforced whoever starts the process |
| Fedora/RHEL | an **SELinux** policy module, which survives files being moved |
| macOS | **launchd** under an unprivileged account with a `sandbox-exec` profile |
| Windows | a **virtual service account** with no privileges and a restricted token, a locked-down data directory ACL, a scoped firewall rule, and WDAC/AppLocker for code integrity |

The intended result: a defect that yields arbitrary file access still cannot read
`/etc/shadow`, write outside the data directory, execute another program or load
a kernel module.

Landlock **defaults to off** and the shipped systemd unit turns it on. The
reasoning is in [ADR-0017](adr/0017-defence-in-depth-hardening.md): a sandbox
that silently denies a file access produces a failure with no obvious cause, and
that is a bad default for someone running the binary on a laptop.

`scripts/harden-check.sh` reports what is *actually* active for a running
instance, which is not the same claim as what was configured.

## 4. Operator responsibilities

The application cannot do these for you:

* **Terminate TLS**, either by giving the process a certificate or by putting a
  proxy in front. The app refuses to bind a non-loopback address without one of
  the two, but it cannot validate your proxy's configuration.
* **Apply the hardening in `deploy/`.** The application sandboxes itself only
  when asked, and on macOS and Windows it cannot sandbox itself at all - that
  confinement comes entirely from the deployment.
* **Renew certificates.** There is no ACME client in the binary; a
  self-managed certificate expires and nothing renews it for you.
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
* Local mode has no authentication by design; anyone with the user's account has
  the data.
* **In-process hardening is Linux-only.** macOS and Windows have no unprivileged
  self-sandboxing API this application can use, so on those platforms the
  confinement is entirely external and absent if the deployment configuration is
  not applied. The application reports this honestly at start-up and on
  `/healthz` rather than implying protection it does not have.
* **The Landlock read-only path list is deliberately generous** (`/usr`, `/etc`,
  `/proc`, `/sys`, `/dev`, `/run`) so that it does not break on distributions we
  have not tested. A tighter list would confine better.
* **There is no ACME/Let's Encrypt integration**, so self-terminated TLS means
  manual renewal.
* **The full-text index is maintained in code**, not by the database. A future
  write path that forgets to update it would leave entries findable by every
  route except search. The rebuild is the repair; there is no automatic
  detection of drift.
* **An imported calendar event carries no link back to its calendar.** That is
  deliberate — an external system should not own recorded hours — but it means
  the duplicate check on re-import is heuristic rather than exact.
* **An approved week is a status, not an immutable snapshot.** The entries are
  the same rows; what stops them changing is the lock, and the lock is code. A
  restore or direct database access can move a week's total after it was
  approved. The stored submitted total makes such a divergence *visible* — the
  approval queue flags it — but it does not prevent it. A copied, immutable
  snapshot at approval would; it is recorded as a rejected alternative in
  ADR-0023 rather than as an oversight.

## 6. Reporting a vulnerability

Report privately to the maintainer rather than in a public issue, with steps to
reproduce and the affected version. Expect acknowledgement within a few days.
Please do not test against an instance you do not own.
