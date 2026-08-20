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
| 007 exports | An export covers the whole filter and never the page: the screen is paged at fifty rows and a test exports a 137-entry range from three different pages, asserting 137 rows each time. That one is a regression test - the screen's cap used to reach the export. CSV and JSON stream and the other three do not, which is two paths through one handler and therefore the arrangement that drifts, so the same range is exported both ways and the documents compared byte for byte with only the generation stamp masked - a check that catches a difference in ordering, in metadata or in formatting, and did catch the two writers formatting JSON differently. The document formats are asserted to refuse an oversized range with 413 and to name a format that streams, rather than to attempt it. The streaming writers are tested against their collected equivalents for identical output, for propagating a mid-stream database error instead of emitting a truncated document that parses, for producing valid JSON when the range is empty, for escaping a title containing a quote, and for refusing entries that arrive out of order - the one input for which the one-pass elapsed total would be silently wrong. A streamed export of a malformed regular expression is asserted to be a 400 that is not typed as the download it refused, which without the pulled first row was a 200 CSV containing its header and nothing else; the pull itself is tested for telling an early failure apart from an empty range, and for stopping the source when a consumer abandons the download. Golden files per format; PDF page-break and repeating-header cases; DOCX opened and validated as a ZIP with well-formed OOXML parts; CSV BOM and non-ASCII names; JSON validated against the published schema. Markdown is checked for the property the format lives or dies by - every row of a table has as many cells as its header - plus escaping of the pipe that would otherwise split a cell, and padding by rune rather than byte so a table containing a non-ASCII name is not ragged. One test walks the offered format list and writes every one of them, because a format offered on screen with no writer behind it reaches a user as an empty download, which nobody reports as a bug. |
| 008 proxy consent | A pending proxy entry appears in **no** total, report or export for the subject; accept makes it count; reject retains it; a test enumerates every aggregation query to assert the status filter is applied. |
| 009 themes and accessibility | The logotype's two halves are aliases of the entity palette, so the contrast helper resolves one level of `var()` the way the cascade would, and both halves are held to AA on the header surface in every theme. Contrast ratios are computed from the stylesheet by the WCAG 2.1 formula and asserted for every text pair the interface uses, **in every theme** rather than only in the one named for contrast - two themes were below AA until that test was written, because looking at a colour is not a way of measuring it. The high-contrast theme additionally has its borders held to 3:1. Entity tints are recomputed in Go at each mix level the stylesheet uses, for every colour in every theme, so colouring a whole row cannot make the text on it unreadable. The contrast maths is itself verified against known reference points (black on white is exactly 21:1, `#767676` on white is the 4.5:1 boundary). Structural checks cover the skip link's position and target, the landmarks and their names, `aria-current` on the current page, an accessible name on every form control, `aria-live` on the totals, and text alternatives wherever colour or an icon carries meaning. None of this replaces the manual screen-reader pass in §5. |
| 010 durability | Migrations apply from empty and from each released schema version; a tampered checksum fails startup; a newer database version fails startup; backup → wipe → restore reproduces database *and* attachments, with the receipt bytes compared rather than counted. Archive tests cover the round trip, that an encrypted archive does not contain its plaintext, that several wrong passwords are all refused, that a flipped bit is detected, and that restoring twice does not double the week or duplicate a receipt. Two guard the interoperability a round trip cannot see: the AE-2 header fields other archivers key on - method 99, zero CRC, no data descriptor, the encrypted flag bit, the 0x9901 extra field - and a known-answer vector pinning the counter as little-endian, since decrypting our own output would work equally well with it reversed. |
| 011 readability | `gofmt -l` empty, `go vet` clean, linter clean; a doc-comment check on exported symbols and package comments. |
| 012 performance | Against 100k entries seeded across three years: day view, week view and timer start/stop under 100 ms p95; a one-year report under 2 s. Measured through the HTTP handler rather than against the store, because the requirement's rationale is that it validates the server-rendered choice - a store benchmark would leave template rendering unmeasured. Every figure is logged whether or not it passes, so a run is a trend as well as a gate. Tagged `perf` and excluded from `make check` because it takes about a minute; `make test-perf` runs it. A three-year export additionally has its *memory* asserted, not its speed: peak heap is sampled while the export runs and held under 64 MB, which 40 MB of JSON output meets at about 9 MB and the same export buffered does not, at 294 MB. That assertion was checked by restoring the buffering and watching it fire. The suite found all three interactive budgets breached by three to four times on first writing, and the causes are recorded in [ADR-0032](adr/0032-measured-before-tuned.md). |
| 013 attachments | Upload/paste round trip; deduplication of identical content; a user without access to the owning record gets 404, not 403; type sniffing rejects a renamed executable; oversize rejection; orphan sweep. SVG is stored rather than refused since ADR-0031, and the store test now pins what replaced the refusal: the file must be labelled `image/svg+xml` rather than the `text/plain` Go's sniffer returns, because every later decision keys on that type - and the detection is asserted narrow, so a note that merely mentions `<svg>` is not promoted to an image. Formats the standard sniffer misses (TIFF both byte orders, OLE documents) have their own test, because each was on the accepted list and unreachable. Preview tests cover the element chosen per type, TIFF transcoding and its pixel bound, DOCX text extraction including elements from other namespaces that share the name `t`, and the stripping of bidirectional overrides from a text preview. |
| 016 localisation | A catalogue parity test fails the build when any language lacks a key. Every screen is rendered in every catalogued language and scanned for leaked keys. Formatter tests pin Swedish decimals, group separators, durations and money against English. Negotiation tests cover quality values, region variants and an unsupported header. |
| 017 help | Every help screen renders in every language. The help control is asserted to be a real link that returns a whole page without JavaScript. An unknown screen falls back rather than erroring. The markup renderer is tested against script injection, since help text is written by translators and its output is marked as trusted HTML. |
| 015 least privilege | Config tests assert that server mode refuses a public bind without TLS, that a half-configured TLS pair is rejected, that a group-readable private key is refused with a message saying how to fix it, and that no CBC or non-forward-secret cipher suite is offered. The platform profiles in `deploy/` are **not** covered by automated tests - they are verified by `scripts/harden-check.sh` and `systemd-analyze security` against a real deployment, which is stated plainly rather than implied. |
| 020 approval and correction | The lock is proved by removing it: a sanity run that neuters `checkPeriodOpen` must fail create, update, delete, timer start and the HTTP-level test together - a lock is only real if every mutation asks. Positive cases cover submit → withdraw → edit, edit refused *into* a locked week as well as inside one, an administrator being refused like anyone else, an empty week refused for submission, rejection requiring a reason, the reason reaching the owner's screen, a resubmission starting clean, nobody deciding on their own week in either mode, and reopen restoring the ability to edit. Week-start arithmetic is tested against Sunday-start weeks and a zone where late Sunday in UTC is already Monday locally. |
| 021 contract terms | Resolution is pinned as a table: which revision is in force on which day, including the day a revision starts and a revision agreed for the future. The project-over-customer merge is tested for the case that motivated it - a project that names only its overtime keeps following the account for everything else - and for the case that required the enumerations to gain an explicit default, where a project overrides a customer that does not pay for travel. A backdated entry is proved to price at the terms in force then, and moving it across a boundary to re-price. |
| 021 contract rules | The rate table is exercised for every kind against every combination of set and unset rule, because an unset rule silently behaving as zero would bill work at nothing - the most expensive failure available here. Quantity pricing is pinned exactly (42.5 km at 25.00 is 106250 minor units, not approximately that) and the formatter is proved to round-trip through the parser, so what is displayed can be typed back. Service tests prove the kind survives storage - it once did not, see §3a - that unbilled travel is still recorded in full, that a threshold produces a notice and marking the time stops it, that an explicit 0% markup is not overwritten by a customer default, and that a claim over the evidence threshold refuses the week's submission by name. |
| 022 learnability | Every guide topic is asserted to exist in every catalogued language, since a missing one renders as its own key - a page of gibberish rather than an error. The proxy topic is checked for the specific promises that make it worth having (a proposal, the inbox, the `@name` syntax, the shared project). Topics that cannot apply in the running mode are asserted absent. The markup renderer is tested for what it must and must not do: lists where every line is a marker, prose otherwise, and HTML from the catalogue escaped rather than executed. |
| 023 routines | The property the design rests on is proved by absence: having a routine creates nothing until it is applied. Weekday matching is tested across a whole week including Sunday, which is 0 in Go and 7 in the stored list. Applying all skips what is already recorded, and a routine cannot be applied by anybody but its owner. |
| 024 calendar import | The parser is tested against what Google and Outlook actually emit - folded lines with a space and with a tab, TZID parameters, DURATION instead of DTEND, bare line feeds, escaped separators - because those are the details that silently truncate a meeting name. The service tests are about judgement rather than parsing: every cancelled, declined, all-day and recurring event is accounted for by name; matching produces one candidate or none; a preview writes nothing; a re-import detects what is already there; a failure on one event does not fail the rest. |
| 025 search | Each mechanism is tested for the property that justifies it, not merely for returning rows: trigram finds a fragment inside a word, a two-character query falls back rather than returning nothing, a query containing FTS5 operators is matched literally, and a regular expression is a regular expression with a malformed one reported as the searcher's mistake. The index is proved to follow edits, tag renames and deletions. |
| 026 idle time | The arithmetic is pinned as intervals rather than as durations, because a resolution that gets the total right and the interval wrong shows up on the timeline instead of in the numbers: keep, discard and split are each checked against an entry with the observed stretch leading, interior and trailing. One property test asserts that whatever the answer, what is kept plus what is removed is the entry - a resolution that loses a minute to arithmetic makes a timesheet stop adding up to the day. The refusals are tested as carefully as the answers: a running timer cannot be resolved (its interval is still being measured), a stretch covering the whole entry offers no answer that would empty the row, and a stretch reported twice is one question. Service tests prove the rules that make the feature safe rather than merely working - a submitted week refuses a discard, a colleague can neither file nor answer an observation about somebody else's entry and cannot see one, and switching the feature off stops it storing anything. The HTTP tests cover what a browser may claim: a report that will not parse is a 400 rather than a silent 204, an unknown resolution is refused rather than treated as one of the three, and the two invisible attributes the watcher needs - the threshold on the body, the entry id on each running timer - are asserted, since nothing but a test notices when one goes missing. |
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
| PDF and DOCX were absent from the Entries screen | the writers were finished in layer 5 and the template still listed only CSV and JSON, under a hint saying the other two arrived later |
| a downloaded export covered more than the screen it was taken from | the export links were assembled by hand per format and carried `customer` but not the tags, the kind or the search query |
| an encrypted archive was structurally perfect and no other tool could open it | the general-purpose "encrypted" flag bit was never set, so an archiver handed the ciphertext straight to its inflater |
| every attachment in a backup lost its file extension | a guard meant to reject path separators passed `".."` to `ContainsAny`, which matches the dot in every extension |
| no TIFF could ever be uploaded | `image/tiff` was on the accepted list, but Go's content sniffer has no TIFF rule and returns `application/octet-stream`, which the same list refuses |
| the day pane said "nothing recorded yet" on a full day | with a fixed window and everything outside it, the empty state was rendered without asking whether anything had been pushed out |
| `make test-perf` passed with no performance suite behind it | the target ran `-run TestPerf` and no test of that name existed, so it exited 0 in silence while TEST.md claimed ASR-012 was proved by it |
| an export of a long range returned 500 | every entry id in the tag lookup is a bound parameter, and SQLite rejects the statement past its ceiling; unreachable until the export stopped being truncated at a thousand rows |
| an export silently dropped everything past the first 1000 entries | the Entries screen's row cap was part of the filter, and the export handler used the same filter - so a long range was truncated oldest-first, and by a different amount depending on which page the screen was on |
| every screen took 300 ms against a realistic database | three queries on the page shell each walked every entry the user had: a partial index the planner declined, a predicate that did not match its index, and `date()` wrapped around an indexed column ([ADR-0032](adr/0032-measured-before-tuned.md)) |
| a kind was applied to a rate but never stored | the service kept its own copy of the entry insert, and the copies drifted |
| a migration wrote timestamps the reader refused | `datetime()` separates date and time with a space; no fresh-database test could see it |

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

The data-carrying migration is a third kind: a statement that only executes when
there is data to carry, in a suite that always starts from an empty database.
`TestMigrationsUpgradeExistingData` applies the schema in two halves with real
rows in between, which is what an upgrade actually is, and then reads every
timestamp column in the database back through the parser the application uses.
`TestMigrationsWriteTimestampsInTheStoredFormat` is the cheap general guard for
the same defect and needs no data at all.

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
* Each of the eight themes viewed on each screen — automated contrast checking does
  not catch "this looks wrong".
* Keyboard-only traversal of the primary flows, and a screen-reader pass over Today
  and Week.
* Clipboard paste of an image on macOS, Windows and Linux browsers — clipboard
  behaviour differs per platform and is not reproducible in `httptest`.
* A restore drill: take a backup, destroy the installation, restore, verify.
* **The timeline**, in a real browser: that a block can be dragged to move it
  and its edge dragged to resize it, that a plain click still opens the
  correction screen, that the fallback forms are visible with JavaScript
  disabled and hidden without, and that the page reports no content-security
  violations. The geometry itself is Go and has ordinary tests; only the
  gestures need a browser.
* **The header clock**, in a real browser: that it ticks; that it still shows the
  right time with JavaScript disabled entirely; and that sabotaging the first
  initialiser leaves the clock and every later feature running, with the failure
  logged. The Go tests cover what is served, which is what makes the placeholder
  failure impossible; only a browser can show that the script does its half.
* **Attachment previews**, in a real browser: that every kind decodes and
  appears, and — the one that matters — that an SVG carrying a `<script>` fires
  nothing, both inside the `<img>` the page uses and when navigated to directly.
  Go tests cover the headers; only a browser can show the headers work.
* **The icons on a real device.** Add the site to an iOS home screen and to an
  Android one, and look at the tab in a browser that prefers PNG to SVG. The Go
  tests hold the rules that can be computed - opacity, declared sizes, the
  maskable safe zone, an installable manifest - but whether the mark reads at 16
  pixels, and whether the platform's own rounding suits it, only a screen shows.
* **An encrypted backup archive opened in another tool.** This one is not
  optional. A round trip through our own reader passes with a counter running
  the wrong way, a missing flag bit, or a mislabelled method — the archive is
  then perfect and useless. Open one in 7-Zip or another AES-capable archiver
  and read a file out of it.

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
