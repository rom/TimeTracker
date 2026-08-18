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
| 009 themes and accessibility | Contrast ratios are computed from the stylesheet by the WCAG 2.1 formula and asserted for every pair the interface actually uses; the contrast maths is itself verified against known reference points (black on white is exactly 21:1, `#767676` on white is the 4.5:1 boundary). Structural checks cover the skip link's position and target, the landmarks and their names, `aria-current` on the current page, an accessible name on every form control, `aria-live` on the totals, and text alternatives wherever colour or an icon carries meaning. None of this replaces the manual screen-reader pass in §5. |
| 010 durability | Migrations apply from empty and from each released schema version; a tampered checksum fails startup; a newer database version fails startup; backup → wipe → restore reproduces database *and* attachments. |
| 011 readability | `gofmt -l` empty, `go vet` clean, linter clean; a doc-comment check on exported symbols and package comments. |
| 012 performance | Against 100k entries: day view, week view and timer start/stop under 100 ms p95; a one-year report under 2 s. Tagged `perf`, run in CI on a fixed runner, reported as a trend rather than a hard gate. |
| 013 attachments | Upload/paste round trip; deduplication of identical content; a user without access to the owning record gets 404, not 403; type sniffing rejects a renamed executable; SVG handling; oversize rejection; orphan sweep. |
| 016 localisation | A catalogue parity test fails the build when any language lacks a key. Every screen is rendered in every catalogued language and scanned for leaked keys. Formatter tests pin Swedish decimals, group separators, durations and money against English. Negotiation tests cover quality values, region variants and an unsupported header. |
| 017 help | Every help screen renders in every language. The help control is asserted to be a real link that returns a whole page without JavaScript. An unknown screen falls back rather than erroring. The markup renderer is tested against script injection, since help text is written by translators and its output is marked as trusted HTML. |
| 015 least privilege | Config tests assert that server mode refuses a public bind without TLS, that a half-configured TLS pair is rejected, that a group-readable private key is refused with a message saying how to fix it, and that no CBC or non-forward-secret cipher suite is offered. The platform profiles in `deploy/` are **not** covered by automated tests - they are verified by `scripts/harden-check.sh` and `systemd-analyze security` against a real deployment, which is stated plainly rather than implied. |
| 020 approval and correction | The lock is proved by removing it: a sanity run that neuters `checkPeriodOpen` must fail create, update, delete, timer start and the HTTP-level test together - a lock is only real if every mutation asks. Positive cases cover submit → withdraw → edit, edit refused *into* a locked week as well as inside one, an administrator being refused like anyone else, an empty week refused for submission, rejection requiring a reason, the reason reaching the owner's screen, a resubmission starting clean, nobody deciding on their own week in either mode, and reopen restoring the ability to edit. Week-start arithmetic is tested against Sunday-start weeks and a zone where late Sunday in UTC is already Monday locally. |
| 021 contract rules | The rate table is exercised for every kind against every combination of set and unset rule, because an unset rule silently behaving as zero would bill work at nothing - the most expensive failure available here. Quantity pricing is pinned exactly (42.5 km at 25.00 is 106250 minor units, not approximately that) and the formatter is proved to round-trip through the parser, so what is displayed can be typed back. Service tests prove the kind survives storage - it once did not, see §3a - that unbilled travel is still recorded in full, that a threshold produces a notice and marking the time stops it, that an explicit 0% markup is not overwritten by a customer default, and that a claim over the evidence threshold refuses the week's submission by name. |
| 022 learnability | Every guide topic is asserted to exist in every catalogued language, since a missing one renders as its own key - a page of gibberish rather than an error. The proxy topic is checked for the specific promises that make it worth having (a proposal, the inbox, the `@name` syntax, the shared project). Topics that cannot apply in the running mode are asserted absent. The markup renderer is tested for what it must and must not do: lists where every line is a marker, prose otherwise, and HTML from the catalogue escaped rather than executed. |
| 014 exactness | Rounding boundary tables (exactly on the increment, one second either side, zero, negative guard); rate × duration half-away-from-zero; mixed-currency addition panics/errors; a source-level check that no persisted field or total is `float64`. |

## 3a. Regression tests

`internal/web/regression_test.go` collects a test for every defect that reached a
running build, named for its symptom rather than for the code it touches. The
list is short and each entry is specific:

| Symptom | Cause |
|---|---|
| a completed customer form rejected as empty | `ParseForm` does not parse a multipart body but does set `r.Form`, so `FormValue` never fell back |
| every screen with entries returned 500 | a fragment rendered inside a `range` lost `$`, and the test logger hid the error |
| the day view went blank | a query was not passed the actor's scope, which correctly renders as "match nothing" |
| PDF and DOCX returned 501 after being implemented | the handler's switch still listed them as unimplemented |
| a shell script named `.png` was stored | SECURITY.md claimed an extension check that did not exist |
| the macOS and Windows builds broke | a symbol used by shared code was defined only in a `_linux.go` file |
| editing an entry was unreachable | the service, the routes and the form existed; no screen rendered a link to any of them |
| the header clock showed `--:--:--` forever | a placeholder waiting for a script, in the browser's zone, behind an init sequence where one throw disabled everything after it |
| the inbox returned 500 with any proposal in it | a custom template function named `index` shadowed the builtin, so a map lookup failed |
| a kind was applied to a rate but never stored | the service kept its own copy of the entry insert, and the copies drifted |

Two of those are worth dwelling on. The 500 was invisible because the test logger
discarded output and the smoke test rendered only empty pages - so the regression
test creates data first. And the extension check was a documentation claim that
was simply untrue, which is the kind of defect only reading the docs against the
code will find.

The unreachable edit form is the same class of defect as the extension check:
every layer passed its own tests, and the feature was still absent from the
product. Its regression test asserts the **link on the row**, not the handler -
the handler was never the broken part. A companion test covers the trap that made
the form wrong as well as unreachable: the end-time field must stay empty,
because an end time overrides a duration and a prefilled one would silently
discard the correction the user had just typed.

Three of these share a shape worth naming: **the page renders fine while it is
empty.** The shadowed `index`, the earlier lost-`$` failure, and the expenses
screen all worked until there was a row on them. Smoke tests that load every
screen on an empty database prove nothing about any of them, so the regression
tests here seed a row first and the broad smoke test now covers the screens that
have one.

The header clock is the other kind. It could not be reproduced in a headless
browser at all - it ticks there - so the fix was not to chase the trigger but to
remove the failure mode: the value is rendered by the server, so there is no
state in which the page shows dashes. The Go test asserts that what is served is
the current time rather than a placeholder, which is a property no browser is
needed to check. The browser-level behaviour (ticking, ticking with the first
initialiser sabotaged, and correct output with JavaScript disabled entirely) was
verified by hand; see §5.

The cross-compile failure is the one no test can catch: `make build-check`
compiles all six OS/arch targets and runs inside `make check`, and a regression
test asserts that it is still wired in, since a Makefile edit could silently
remove it.

## 3b. Coverage

`make coverage` produces a profile and an HTML report; `make coverage-check`
enforces per-package floors and runs as part of `make check`.

The floors are on the packages where a gap is expensive - the domain rules, the
service layer that enforces authorisation and audit, the store, the exporters and
the blob store. There is deliberately **no global percentage target**: chasing one
produces tests for trivial accessors while an authorisation branch goes
unexercised. The floors are set just below the current figures, so a regression
fails the build while ordinary work does not, and lowering one is expected to
come with a note saying why.

`internal/web` is deliberately excluded: its coverage is dominated by template
rendering that the HTTP tests exercise thoroughly without the statement counter
noticing.

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
* **The header clock**, in a real browser: that it ticks; that it still shows the
  right time with JavaScript disabled entirely; and that sabotaging the first
  initialiser leaves the clock and every later feature running, with the failure
  logged. The Go tests cover what is served, which is what makes the placeholder
  failure impossible; only a browser can show that the script does its half.

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
