# ADR-0003: SQLite through a pure-Go driver

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-002, ASR-003, ASR-012

## Context

The data is relational (customers, projects, assignments, entries, users, roles),
small to medium in volume, and must be queried with date-range aggregation. It has
to work identically for one user on a laptop and a small team on a server. ASR-002
requires cross-compilation to macOS, Linux and Windows on amd64 and arm64 without a
C toolchain, and ASR-003 forbids requiring a database server.

The best-known Go SQLite driver, `mattn/go-sqlite3`, binds the C library through
cgo. Cgo means a working cross-compiler per target — the single largest obstacle to
"one `make` target builds every platform".

## Decision

We will use **SQLite** as the only supported database, accessed through
**`modernc.org/sqlite`**, a pure-Go transpilation of the SQLite C sources. No cgo,
so `GOOS=windows GOARCH=arm64 go build` works from any host.

Access goes through `database/sql` with hand-written SQL. Connection settings are
fixed at open time and are not optional:

* `journal_mode=WAL` — readers do not block the writer, which matters as soon as a
  report runs while someone stops a timer;
* `busy_timeout=5000` — wait rather than fail on contention;
* `foreign_keys=ON` — SQLite disables these per connection by default, and silent
  orphan rows are exactly the corruption we cannot detect later;
* a **single** writer connection, with reads on a pool — SQLite permits one writer,
  and serialising in the pool is cheaper than handling `SQLITE_BUSY` everywhere.

We write plain SQL rather than using an ORM. The queries are the interesting part of
this application and hiding them behind generated methods works against ASR-011.

## Consequences

**Positive**

* True single-file, zero-dependency deployment, cross-compiled from one host.
* The user's data is one file they can copy, back up, and inspect with any SQLite
  tool.
* Transactions give us the consistency needed for approval workflows and audit
  writes.

**Negative / accepted costs**

* The pure-Go driver is measurably slower than the C library — roughly a small
  constant factor. At the scale in ASR-012 (100k entries) this is irrelevant, and
  correct indexing matters far more than driver overhead.
* It tracks upstream SQLite with a lag, so brand-new SQLite features are not
  immediately available. We use no exotic features.
* Single-writer means a long-running write transaction blocks other writes. Service
  methods must keep transactions short and never hold one across an HTTP round trip.
* Concurrency ceiling is a small team, not hundreds of simultaneous writers.

## Alternatives considered

**`mattn/go-sqlite3` (cgo)** — faster and canonical. Rejected squarely on ASR-002:
cross-compiling it needs a C toolchain per target, which makes the release process
the most fragile part of the project.

**PostgreSQL** — the right answer at a larger scale, and better under write
concurrency. Rejected for now on ASR-003: requiring a database server destroys the
laptop use case. The repository layer is kept behind interfaces with SQL isolated in
one package, so this stays a future ADR rather than a rewrite.

**An embedded Go key-value store (bbolt, Badger)** — no SQL, no driver concerns.
Rejected because reporting is inherently relational and ad-hoc aggregation; we would
end up writing a query engine.

**An ORM (GORM, ent)** — less boilerplate. Rejected on ASR-011: the generated or
reflective query path is harder to read and to reason about for performance than the
SQL it replaces.

## Related

* ADR-0009 (migrations), ADR-0012 (package structure)
