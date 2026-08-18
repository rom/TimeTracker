# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Architecturally significant changes reference the [ADR](adr/README.md) that records
the decision.

## [Unreleased]

### Added

* **Documentation set.** `MVP_PLAN.md`, `DESIGN.md`, `ARCHITECTURE.md`, `TEST.md`,
  `SECURITY.md` and this changelog.
* **Architecturally Significant Requirements register** ([ASR.md](ASR.md)) — 14
  requirements, each with a quality attribute, an objective fit criterion, a
  rationale and the ADRs that realise it.
* **Architecture Decision Records** ([adr/](adr/README.md)) — 15 accepted records
  with an index, a template and the process rules (one decision per record,
  immutable once accepted, superseded rather than edited):
  * ADR-0001 single binary with two run modes
  * ADR-0002 server-rendered HTML with HTMX, no SPA
  * ADR-0003 SQLite through a pure-Go driver
  * ADR-0004 multiple concurrent timers, overlaps allowed
  * ADR-0005 proxy entries require the subject's confirmation
  * ADR-0006 local accounts plus OIDC, session cookies
  * ADR-0007 pure-Go PDF and DOCX generation
  * ADR-0008 four roles scoped by project membership
  * ADR-0009 embedded assets and forward-only migrations
  * ADR-0010 append-only audit log, rsyslog via RFC 5424
  * ADR-0011 theming via CSS custom properties only
  * ADR-0012 layered packages with a service boundary
  * ADR-0013 content-addressed attachments on disk
  * ADR-0014 integer minor units and whole seconds
  * ADR-0015 timestamps stored in UTC, displayed local
* **Makefile** producing `bin/timetracker`, with cross-compilation for
  macOS/Linux/Windows on amd64 and arm64, plus test, lint, vet, fmt, coverage and
  clean targets.
* Go module and layered package skeleton.

[Unreleased]: https://github.com/rom/timetracker/commits/main
