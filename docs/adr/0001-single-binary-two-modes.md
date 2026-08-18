# ADR-0001: Single binary with two run modes

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-004, ASR-003

## Context

The application has two quite different deployments:

* **Local** — one person, on their own laptop, tracking their own time. Nobody else
  can reach the process. A login screen here is pure friction.
* **Server** — a shared instance for a team, reachable over a network, requiring
  authentication, authorisation, approval workflows and audit logging.

The obvious approaches are two separate programs, or one program that behaves
differently. Two programs drift: a bug fixed in one is forgotten in the other, and
features get built twice. But a single program that scatters `if serverMode` checks
through its handlers is worse — every such check is a place where the local path
accidentally skips an authorisation rule that the server path enforces.

## Decision

We will ship **one binary** with a `--mode` flag (`local` by default, `server`
explicitly). The mode is resolved exactly once, at startup, into a set of injected
collaborators:

* an **identity provider** — in local mode a fixed single-user identity; in server
  mode a session/OIDC-backed one;
* an **authoriser** — in local mode one that grants the owner everything about their
  own data; in server mode the RBAC authoriser from ADR-0008;
* a **log sink** — stderr in local mode, stderr plus rsyslog in server mode.

Handlers and services never inspect the mode. They call `auth.Can(ctx, action,
resource)` and get an answer. Local mode is therefore not "authorisation turned
off"; it is a trivially permissive authoriser satisfying the same interface, which
means the authorisation code path is exercised in both modes.

Defaults differ by mode where safety demands it: local binds `127.0.0.1` and may
open a browser; server requires an explicit bind address, refuses to start without a
configured session secret, and will not bind a non-loopback address without TLS
being terminated in front of it.

## Consequences

**Positive**

* One feature set, one test suite, no drift.
* The authorisation code path is never bypassed, only parameterised — a whole class
  of "works locally, leaks on the server" bugs cannot occur.
* A user can graduate from laptop to server by copying the database file.

**Negative / accepted costs**

* Every handler must obtain identity from the request context rather than assuming
  one, which is slightly more ceremony in single-user mode.
* The binary carries server-mode code (OIDC, sessions, syslog) that a laptop user
  never executes. At a few hundred kilobytes this is a price worth paying.
* Two sets of defaults means two sets of start-up paths to test.

## Alternatives considered

**Two binaries from a shared library** — genuinely tempting, and it makes the local
binary smaller. Rejected because the shared library boundary would inevitably sit in
the wrong place: authorisation is exactly the layer that would live above the shared
code, and that is the layer most in need of shared testing.

**Build tags** (`//go:build server`) — same drift problem as two binaries, plus the
local build cannot be tested for server behaviour in CI without a second compile,
and IDEs and linters routinely ignore one side of a build tag.

**Server mode only, with a trivial local user** — simplest code, but it forces a
laptop user through a login screen and a session secret they have no reason to care
about, for no security benefit on a loopback-only port.

## Related

* ADR-0006 (authentication model), ADR-0008 (RBAC), ADR-0010 (logging)
