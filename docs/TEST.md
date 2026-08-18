# Test Strategy

> What we test, why, and how to run it. The guiding rule: **every fit criterion in
> [ASR.md](ASR.md) must be checked by something automated**, otherwise it is an
> aspiration rather than a requirement.

## 1. Principles

1. **Test behaviour, not implementation.** A refactor that keeps behaviour should
   not break the suite.
2. **The security-relevant tests are not optional.** Authorisation, audit and
   isolation tests are the ones that must never be skipped or weakened to make CI
   green.
3. **Deterministic.** No test depends on wall-clock time, network access or
   execution order. Time is injected through a clock interface; randomness is
   seeded.
4. **Fast enough to run constantly.** The unit and service suites finish in
   seconds; anything slower is tagged and run separately.
5. **A bug gets a test first.** Every fixed defect leaves behind the test that would
   have caught it.

## 2. Levels

| Level | Scope | Speed | Where |
|---|---|---|---|
| **Unit** | `internal/domain`, parsers, `Money`, rounding, time zone helpers | ms | pure Go, no fixtures |
| **Store** | SQL, migrations, indexes, constraints | fast | real SQLite in a temp dir — never a mock, since SQL semantics are what we are testing |
| **Service** | business rules, authorisation, audit atomicity, transactions | fast | real store, in-memory or temp database |
| **HTTP** | routing, middleware, rendering, forms, HTMX fragments | fast | `httptest`, no browser |
| **Golden file** | CSV, JSON, PDF, DOCX output | fast | byte comparison against committed fixtures |
| **Architecture** | layering rules from [ADR-0012](adr/0012-layered-package-structure.md) | ms | import graph analysis |
| **Performance** | ASR-012 budgets | slow, tagged | generated 100k-entry dataset |
| **Cross-platform** | macOS, Linux, Windows × amd64/arm64 | CI | build + full suite per platform |

## 3. What each ASR is proved by

| ASR | Test |
|---|---|
| 001 parallel tracking | Three timers started concurrently all persist and report; overlapping intervals survive a round trip; summed and elapsed totals differ correctly for a known overlap; stop is idempotent under concurrent requests. |
| 002 portability | CI matrix builds and runs the full suite on all three OSes and both architectures; a test asserts no cgo (`CGO_ENABLED=0` build) and that embedded tzdata resolves `Europe/Stockholm` without a system zoneinfo. |
| 003 single binary | Build with `CGO_ENABLED=0`, run the binary in an empty directory with no source tree present, assert the UI serves and migrations apply. |
| 004 two modes | The same service test suite runs against the permissive and the RBAC authoriser; a test asserts no package outside `cmd` reads the run mode. |
| 005 authorisation | The RBAC matrix: every role × every action × in-scope and out-of-scope resource. Plus a reflective test that enumerates service methods and fails on any that never calls `Can()`. |
| 006 audit | For each mutating service method: the change and its audit row commit together; an injected failure in the audit write rolls back the change; no code path updates or deletes `audit_event`; a redaction test asserts secrets never reach either sink. |
| 007 exports | Golden files per format; PDF page-break and repeating-header cases; DOCX opened and validated as a ZIP with well-formed OOXML parts; CSV BOM and non-ASCII names; JSON validated against the published schema. |
| 008 proxy consent | A pending proxy entry appears in **no** total, report or export for the subject; accept makes it count; reject retains it; a test enumerates every aggregation query to assert the status filter is applied. |
| 009 themes | Every semantic token pair used by a component is contrast-checked per theme; the high-contrast theme must meet WCAG AA (4.5:1 body, 3:1 large/UI). Fails on a new token that any theme leaves undefined. |
| 010 durability | Migrations apply from empty and from each released schema version; a tampered checksum fails startup; a newer database version fails startup; backup → wipe → restore reproduces database *and* attachments. |
| 011 readability | `gofmt -l` empty, `go vet` clean, linter clean; a doc-comment check on exported symbols and package comments. |
| 012 performance | Against 100k entries: day view, week view and timer start/stop under 100 ms p95; a one-year report under 2 s. Tagged `perf`, run in CI on a fixed runner, reported as a trend rather than a hard gate. |
| 013 attachments | Upload/paste round trip; deduplication of identical content; a user without access to the owning record gets 404, not 403; type sniffing rejects a renamed executable; SVG handling; oversize rejection; orphan sweep. |
| 015 least privilege | Config tests assert that server mode refuses a public bind without TLS, that a half-configured TLS pair is rejected, that a group-readable private key is refused with a message saying how to fix it, and that no CBC or non-forward-secret cipher suite is offered. The platform profiles in `deploy/` are **not** covered by automated tests - they are verified by `scripts/harden-check.sh` and `systemd-analyze security` against a real deployment, which is stated plainly rather than implied. |
| 014 exactness | Rounding boundary tables (exactly on the increment, one second either side, zero, negative guard); rate × duration half-away-from-zero; mixed-currency addition panics/errors; a source-level check that no persisted field or total is `float64`. |

## 4. Practices

* **Table-driven tests** for anything with a rule set — rounding, duration parsing,
  quick-add parsing, authorisation.
* **Fixtures as builders**, not shared global state: `newCustomer(t, …)` returning a
  fully valid object with named overrides, so a test states only what it cares
  about.
* **A single-line assertion helper set** rather than an assertion framework;
  failures print the actual and expected values and the case name.
* **Clock injection everywhere.** `time.Now()` appears in exactly one place. DST
  transitions are tested with real historical dates in a real zone.
* **Concurrency tests run under `-race`**, and `-race` is on in CI for the whole
  suite.
* **Fuzz** the duration parser, the quick-add parser and the import paths — they
  take arbitrary user text.
* **Golden files are reviewed, not regenerated blindly.** `make test-update-golden`
  exists, and a diff in a golden file is expected to be justified in the pull
  request.

## 5. Manual verification

Some things automation cannot honestly settle, checked before each release:

* Generated DOCX opened in **Microsoft Word** and **LibreOffice Writer**; PDF in at
  least two viewers.
* Each of the seven themes viewed on each screen — automated contrast checking does
  not catch "this looks wrong".
* Keyboard-only traversal of the primary flows, and a screen-reader pass over Today
  and Week.
* Clipboard paste of an image on macOS, Windows and Linux browsers — clipboard
  behaviour differs per platform and is not reproducible in `httptest`.
* A restore drill: take a backup, destroy the installation, restore, verify.

## 6. Running the tests

```sh
make test              # unit, store, service, HTTP, golden, architecture; -race
make test-short        # skips store-heavy tests; the inner development loop
make coverage          # coverage profile + HTML report
make test-perf         # tagged performance suite against a generated dataset
make lint vet fmt      # static analysis and formatting
make check             # everything CI runs
```

## 7. Coverage

Coverage is a diagnostic, not a target. We do not chase a global percentage; we do
require that `internal/domain`, `internal/service` and the authorisation code are
comprehensively covered, and that every branch of an authorisation or audit decision
is exercised. A pull request that lowers coverage in those packages needs a reason.

## 8. Continuous integration

Every push: build (`CGO_ENABLED=0`), `gofmt`/`vet`/lint, full suite with `-race` on
the OS matrix, golden-file diff check, architecture test, and a vulnerability scan
of dependencies (`govulncheck`). The performance suite runs on a schedule rather
than per push, because a shared runner cannot give a trustworthy p95 on demand.
