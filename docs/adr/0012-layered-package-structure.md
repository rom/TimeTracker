# ADR-0012: Layered packages with a service boundary

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-011, ASR-006, ASR-005

## Context

Two requirements pull directly on the code structure. ASR-006 says every mutation
must be audited atomically, and ASR-005 says every access must be authorised. Both
are guarantees of the form "this must happen *every* time", and such guarantees only
hold if there is exactly one place where the thing can happen. A Go web application
that puts SQL in its HTTP handlers has as many places as it has handlers.

ASR-011 pulls the other way: layering for its own sake is how a small application
acquires six files per feature and becomes unreadable.

## Decision

Four layers, the minimum that gives one enforcement point:

```
cmd/timetracker      wiring: flags, config, mode selection, start/stop
  └── internal/web       HTTP: routing, templates, form decoding, rendering
        └── internal/service   business rules, authorisation, audit, transactions
              └── internal/store   SQL, migrations, mapping rows to domain types
                    └── internal/domain  types and invariants, no I/O
```

The rules that make this worth having:

* **Handlers never touch the store.** They call a service method and render the
  result. This is what makes "every mutation is audited and authorised" checkable by
  inspection: the service package is the only importer of the store.
* **The service layer owns transactions.** A service method opens a transaction,
  performs the change and writes the audit row, then commits. Neither the store nor
  the handler starts one, so audit atomicity cannot be forgotten.
* **The domain package has no dependencies** — no database, no HTTP, no logging. It
  holds types and the invariants that are true regardless of storage, which makes
  the interesting rules testable without any fixture.
* **The actor travels in `context.Context`**, resolved once by middleware. Service
  methods take `ctx` and ask the authoriser; they never receive a "current user"
  parameter that a caller could fake, and never consult the run mode (ADR-0001).
* `internal/` is deliberate: this is an application, not a library, and nothing here
  is a stable public API.

Cross-cutting packages (`config`, `logging`, `export`, `blob`) sit beside the layers
and may be imported by any of them, but import none of them, so there are no cycles.

Comment discipline (ASR-011): package comments state responsibility *and*
boundaries — what belongs here and what deliberately does not; non-obvious code
carries a "why", with a reference to the ADR when one exists.

## Consequences

**Positive**

* "Is everything audited?" is answered by reading one package.
* Business rules are testable without HTTP or, mostly, without a database.
* Swapping SQLite for PostgreSQL later is confined to one layer (ADR-0003).

**Negative / accepted costs**

* A trivial read passes through three layers, which is boilerplate for genuinely
  simple screens. Accepted: reads may use thin pass-through service methods, but the
  layer is not skipped, because the exception would immediately be copied for a
  write.
* More files and more indirection than a two-layer handler-plus-SQL design.
* Domain types risk being duplicated as store rows and view models. We keep one set
  of domain types and let the layers above shape them for display, rather than
  defining parallel structs at each level.

## Alternatives considered

**Handlers calling SQL directly** — the shortest path, and fine for a CRUD toy.
Rejected: it makes ASR-005 and ASR-006 unprovable, since every new handler is a new
chance to forget.

**Full hexagonal architecture with ports and adapters both ways** — more testable in
principle. Rejected on ASR-011: interface indirection for a single implementation
costs readability and buys nothing here.

**Package-per-feature (`internal/timeentry/{http,service,store}.go`)** — good
cohesion, and a fair alternative. Rejected because the single audit/authorisation
enforcement point then exists once per feature package, which is the property we
were trying to avoid.

## Related

* ADR-0003, ADR-0008, ADR-0010
