# ADR-0006: Local accounts plus OIDC, session cookies

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-005, ASR-004

## Context

Server mode is multi-user and network-reachable, holding commercially sensitive
data. It may be deployed by a two-person consultancy with no identity infrastructure
at all, or inside a company that already runs Entra ID, Google Workspace or
Keycloak and expects SSO. Both must work, and neither should require the other.

Local mode must not ask for a password: the process is bound to loopback and serves
exactly the person who started it.

## Decision

Two authentication mechanisms in server mode, both resolving to the same `User`
record and the same session:

1. **Local accounts** — passwords hashed with **Argon2id** (memory-hard, current
   OWASP-recommended parameters, per-user random salt, parameters stored with the
   hash so they can be raised later and rehashed on next successful login). Login is
   rate-limited per account and per source address, with a constant-time comparison
   and a uniform "invalid credentials" response so the endpoint does not disclose
   which accounts exist.
2. **OIDC (OpenID Connect) Authorization Code flow with PKCE** — against any
   compliant provider. Discovery via the issuer's well-known document; `state` and
   `nonce` verified; ID token signature and claims validated. Accounts are linked by
   the immutable `sub` claim, never by email address, which is mutable and
   re-assignable. Role assignment can optionally be mapped from a configured claim,
   with a documented default for unmapped users (`member`, no project memberships —
   deliberately the least privilege).

Sessions are **server-side records** referenced by an opaque, high-entropy cookie
(`HttpOnly`, `Secure`, `SameSite=Lax`, host-only, no `Domain` attribute). Not JWTs:
server-side sessions can be revoked instantly, which matters when someone leaves.
Session ID is rotated on privilege change and on login to defeat fixation. Absolute
and idle lifetimes are both enforced. Every state-changing request carries a CSRF
token bound to the session.

Optional **TOTP** second factor for local accounts, and **API tokens** (hashed at
rest, scoped, individually revocable) for scripted exports. Local mode uses a
built-in single identity and no session at all.

## Consequences

**Positive**

* A small team can run it with no external dependency; a larger org gets SSO,
  central offboarding and their existing MFA.
* Server-side sessions mean revocation is immediate and does not wait for a token to
  expire.
* Linking on `sub` avoids the account-takeover path where an email is reassigned in
  the directory.

**Negative / accepted costs**

* Two authentication paths to secure, test and document — genuinely more work than
  either alone, and the union of their attack surfaces.
* OIDC requires outbound network access to the provider, and a callback URL that
  must be configured correctly in two places.
* Argon2id costs memory per login by design; parameters must be tuned so a burst of
  logins cannot exhaust RAM on a small server. We bound concurrent hashing.
* Session storage is state in the database, which must be pruned.

## Alternatives considered

**Local accounts only** — least code. Rejected: an org with SSO would have to manage
a parallel set of credentials and would have no way to disable access centrally when
someone leaves.

**OIDC only** — no password handling at all, which is attractive. Rejected: it makes
a two-person consultancy dependent on an external identity provider to open their
own timesheet.

**JWT sessions, stateless** — no session table. Rejected: revocation requires either
a denylist (state again, minus the simplicity) or accepting a window in which a
sacked employee still has access.

**LDAP/AD bind** — fits traditional on-prem estates. Deferred rather than rejected;
it would be a new ADR, and OIDC covers most of the same deployments today.

## Related

* ADR-0001 (two modes), ADR-0008 (RBAC), ADR-0010 (audit log)
