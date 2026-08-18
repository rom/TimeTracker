# Architecture Decision Records

An **ADR** records one architecturally significant decision: the context it was made
in, the decision itself, and the consequences accepted along with it.

## Rules

1. **One decision per record.** If a record needs the word "also", it is two records.
2. **Immutable once accepted.** An accepted ADR is never edited to change its
   decision. Reversing a decision means writing a *new* ADR that supersedes the old
   one; the old record stays, with its status updated to `superseded by ADR-NNNN`.
   The value of an ADR is the reasoning at the time, including reasoning later
   proved wrong.
3. **Every ADR links to the [ASRs](../ASR.md) it realises.** A decision that serves
   no requirement is either missing an ASR or is not architecturally significant.
4. **Alternatives are recorded with why they lost**, not just that they existed.

## Format

Lightweight [MADR](https://adr.github.io/madr/): Status · Context · Decision ·
Consequences · Alternatives considered · Related.

Statuses: `proposed` → `accepted` → `superseded by ADR-NNNN` | `deprecated`.

## Index

| ID | Title | Status | Date | Addresses |
|---|---|---|---|---|
| [0001](0001-single-binary-two-modes.md) | Single binary with two run modes | accepted | 2026-08-18 | ASR-004 |
| [0002](0002-server-rendered-htmx.md) | Server-rendered HTML with HTMX, no SPA | accepted | 2026-08-18 | ASR-003, ASR-012 |
| [0003](0003-pure-go-sqlite.md) | SQLite through a pure-Go driver | accepted | 2026-08-18 | ASR-002, ASR-003, ASR-012 |
| [0004](0004-concurrent-timers.md) | Multiple concurrent timers, overlaps allowed | accepted | 2026-08-18 | ASR-001 |
| [0005](0005-proxy-time-entry.md) | Proxy entries require the subject's confirmation | accepted | 2026-08-18 | ASR-008 |
| [0006](0006-authentication-model.md) | Local accounts plus OIDC, session cookies | accepted | 2026-08-18 | ASR-004, ASR-005 |
| [0007](0007-pure-go-document-generation.md) | Pure-Go PDF and DOCX generation | accepted | 2026-08-18 | ASR-002, ASR-007 |
| [0008](0008-rbac-model.md) | Four roles scoped by project membership | accepted | 2026-08-18 | ASR-005 |
| [0009](0009-embedded-assets-and-migrations.md) | Embedded assets and forward-only migrations | accepted | 2026-08-18 | ASR-003, ASR-010 |
| [0010](0010-audit-log-and-rsyslog.md) | Append-only audit log, rsyslog via RFC 5424 | accepted | 2026-08-18 | ASR-006 |
| [0011](0011-theming-via-css-custom-properties.md) | Theming via CSS custom properties only | accepted | 2026-08-18 | ASR-009 |
| [0012](0012-layered-package-structure.md) | Layered packages with a service boundary | accepted | 2026-08-18 | ASR-006, ASR-011 |
| [0013](0013-attachment-storage.md) | Content-addressed attachments on disk | accepted | 2026-08-18 | ASR-013 |
| [0014](0014-exact-money-and-duration.md) | Integer minor units and whole seconds | accepted | 2026-08-18 | ASR-014 |
| [0015](0015-utc-storage-local-display.md) | Timestamps stored in UTC, displayed local | accepted | 2026-08-18 | ASR-014 |

## Writing a new one

Copy [`template.md`](template.md), take the next free number, add a row above.
