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
| **Golden file** | CSV, JSON and Markdown output | fast | byte comparison against `internal/export/testdata/golden/`; PDF and DOCX are asserted structurally instead |
| **Architecture** | layering rules from [ADR-0012](adr/0012-layered-package-structure.md) | ms | import graph analysis |
| **Source scan** | rules about code that was *not* written: no cgo, nothing rewrites the audit trail, every aggregation excludes unaccepted time, exported symbols are documented, persisted values are integers | ms | `internal/repocheck`, parsing the tree with `go/ast` |
| **Whole binary** | ASR-003: build it, run it in an empty directory with a stripped environment, serve a page, shut down | ~5 s | `internal/repocheck`, skipped under `-short` |
| **Performance** | ASR-012 budgets | slow, tagged | generated 100k-entry dataset |
| **Cross-platform** | macOS, Linux, Windows × amd64/arm64 | CI | build + full suite per platform |

## 3. What each ASR is proved by

| ASR | Test |
|---|---|
| 001 parallel tracking | Three timers started concurrently all persist and report; overlapping intervals survive a round trip; summed and elapsed totals differ correctly for a known overlap; stop is idempotent under concurrent requests. |
| 002 portability | `make build-check` compiles every package for all three OSes and both architectures, with `CGO_ENABLED=0` exported by the Makefile, and a regression test asserts that target is still wired into `make check` - so removing the cross-compile fails the suite. Embedded tzdata resolving `Europe/Stockholm` without a system zoneinfo is covered by the calendar tests. Source scans in `internal/repocheck` now assert the property the flag stands for rather than the flag alone: nothing imports `"C"`, no `#cgo` preamble sits in a comment, there is no C or assembly in the tree, the module still requires the pure-Go driver and requires no cgo one, and nothing outside `internal/hardening` uses a non-portable `syscall` selector - the signal numbers being the stated exception, since those are declared on all three platforms. **Not covered:** nothing *runs* the suite on another OS. There is no CI (see §8), so the matrix is a compile check on one machine rather than an execution matrix. |
| 003 single binary | Migrations are embedded and are applied from empty by every store and service test, each against a fresh temporary database with no fixtures on disk - which is the substance of the claim. `TestTheBinaryStartsInAnEmptyDirectory` now does what the requirement says a user does: it builds the binary, runs it from an empty working directory with a stripped environment - no `TT_` variables, no `PATH` - against an empty data directory, then fetches the health endpoint, a rendered page and the stylesheet, checks the database was created where it should be and that nothing was written into the working directory, and asserts a clean exit on SIGINT. The stylesheet request is the one that would catch a build whose assets came from the source tree rather than from the binary. `timetracker version` is covered separately, in a directory with nothing in it, since that is what somebody runs first. Both are skipped under `-short`. |
| 004 two modes | The same service test suite runs against the permissive and the RBAC authoriser; a test asserts no package outside `cmd` reads the run mode. |
| 005 authorisation | The RBAC matrix: every role × every action × in-scope and out-of-scope resource. Plus two source-level tests over the service package, which now exist - this row claimed the first of them for some time before it did. One enumerates every exported method on the service and fails on any that never reaches the authoriser, following calls through package-local helpers so that delegating to `decideWeek` or `authorizeOwner` counts; the exceptions are named with the reason each is open, and a stale name in that list fails too. The other fails any method returning time entries that does not apply the client projection, which is what keeps ADR-0008's promise true for the *next* read path rather than only for today's. The client projection itself is tested from both sides: at the service, that the note, the rate, the amount, the currency, the rounding rule, the proxy authorship, the tags and the attachment count are absent from the value rather than hidden by a template; and over HTTP, that a real client login gets its own customer's work, gets it narrowed in every export format, is refused every administrative screen and every write, and cannot download a backup - which it could, carrying the customer's negotiated rate, until a test asked. |
| 006 audit | `TestAuditTrailRecordsEveryMutation` walks the mutating service methods and asserts each leaves an audit row, written in the same transaction as the change - so a rolled-back change cannot leave a row claiming it happened. Authentication events are audited separately, and `TestPasswordsNeverAppearInAudit` is the redaction test. Both gaps are now closed, and closing the first one found a defect. `internal/service/audit_test.go` injects a failure with a SQLite trigger that aborts inserts into `audit_events`, and asserts the change is rolled back - and the converse, breaking the entry insert instead, to prove no audit row survives a change that never happened. A trigger rather than a fake store, because what is under test is whether the *database* rolls the other statement back. The scan half lives in `internal/repocheck`: no migration and no query anywhere in the tree issues `UPDATE`, `DELETE FROM` or `DROP TABLE` against `audit_events`, the table does not cascade on delete, and the insert takes a `*sql.Tx` rather than a connection - which is the mechanism, not an observation about one call. **The defect the first of those found, now fixed:** only the entry paths were atomic. The catalogue mutations wrote the record on one connection and then called `recordAudit`, which opened a second transaction - so a failed audit write reported failure to the caller with the change already committed, and a user who retried got two customers and no trail for either. The store's catalogue mutations gained transactional forms, and `Service.mutate` now runs the change and its audit row in one transaction, the same arrangement the entry paths use. Twelve paths are covered by the injection test: start a timer, record an entry, add a customer, a project and an assignment, rename a customer, archive a customer, archive a project, import a CSV, import a calendar, attach a receipt and delete one. The importers and attachments each needed a different answer rather than the same one. A CSV import prepares every row - which is where a refusal names a line in the user's spreadsheet - and then writes them all with their audit rows and the summary in one transaction, so `TestAnImportThatFailsPartWayImportsNothing` can inject a failure against the third row of three and find nothing behind it; that is the single-transaction import ADR-0022 listed as an outstanding want. A calendar import does the same while keeping the per-meeting refusals it is built around, because those happen during preparation, before anything is written. An attachment commits its row and its audit entry together, with the bytes written before the transaction and removed after it: two tests cover the halves, that a rolled-back attachment leaves only an orphaned file the sweep collects, and that a rolled-back deletion leaves the file where it was rather than deleted out from under a surviving row. `auditNotAtomic` is now empty and stays in the file because the test checks it in both directions - a path named there that becomes atomic fails too, so a list of known defects cannot quietly include fixed ones. What is still not atomic is the restore, which rebuilds the catalogue record by record and is already a sequence of individually audited creates, and the account and timesheet paths. |
| 007 exports | An export covers the whole filter and never the page: the screen is paged at fifty rows and a test exports a 137-entry range from three different pages, asserting 137 rows each time. That one is a regression test - the screen's cap used to reach the export. There are golden files for the three text formats, in `internal/export/testdata/golden/`, regenerated with `go test ./internal/export/ -update`: they exist because a structural assertion passes on any output with the right shape, and CSV, JSON and Markdown are read by software somebody else wrote - a reordered column or a renamed header stays well-formed and breaks their importer. The two streaming writers are held to the same files as the collected ones, which catches the case `TestStreamedAndCollectedExportsAgree` cannot: both drifting together. The PDF and the DOCX deliberately have none, since a zip's per-entry metadata and a PDF's byte offsets move with any content change, and a golden file that has to be regenerated for every unrelated edit trains everybody to regenerate without reading. Those two are asserted on their structure instead - a PDF for its page breaks and repeating headers, a DOCX opened as a zip with its OOXML parts parsed, Markdown for the property its tables live or die by, and `TestFormatsAgree` for the totals across all of them. CSV and JSON stream and the other three do not, which is two paths through one handler and therefore the arrangement that drifts, so the same range is exported both ways and the documents compared byte for byte with only the generation stamp masked - a check that catches a difference in ordering, in metadata or in formatting, and did catch the two writers formatting JSON differently. The document formats are asserted to refuse an oversized range with 413 and to name a format that streams, rather than to attempt it. The streaming writers are tested against their collected equivalents for identical output, for propagating a mid-stream database error instead of emitting a truncated document that parses, for producing valid JSON when the range is empty, for escaping a title containing a quote, and for refusing entries that arrive out of order - the one input for which the one-pass elapsed total would be silently wrong. A streamed export of a malformed regular expression is asserted to be a 400 that is not typed as the download it refused, which without the pulled first row was a 200 CSV containing its header and nothing else; the pull itself is tested for telling an early failure apart from an empty range, and for stopping the source when a consumer abandons the download. Golden files per format; PDF page-break and repeating-header cases; DOCX opened and validated as a ZIP with well-formed OOXML parts; CSV BOM and non-ASCII names; JSON validated against the published schema. Markdown is checked for the property the format lives or dies by - every row of a table has as many cells as its header - plus escaping of the pipe that would otherwise split a cell, and padding by rune rather than byte so a table containing a non-ASCII name is not ragged. One test walks the offered format list and writes every one of them, because a format offered on screen with no writer behind it reaches a user as an empty download, which nobody reports as a bug. |
| 008 proxy consent | `TestPendingProxyEntriesAreNotCounted` and `TestReportExcludesPendingFromTotals` assert that a proposal counts for nothing until it is accepted; `TestProxyWorkflow` covers accept, and `TestProxyRejectionIsKept` that a rejection is retained rather than deleted. They are now enumerated as well, in `internal/repocheck`, in both languages the totals are written in. Every function in `internal/store` whose query aggregates over `time_entries` must carry `status = 'confirmed'` and `flagged = 0`, or build its conditions from an `EntryFilter` whose `CountingOnly` the service sets; every function in the service and the export layer that accumulates a duration or an amount must consult `Counts()`. Both have an exemption map naming the reason each exception is not a total - a row count for a paged listing, a count of what is *awaiting* a decision, an assignment ranked by how often it was used, the arrow that reports how much work fell outside the visible window - and both check the map against the tree, so a name that no longer exists fails rather than quietly excusing the next function given it. |
| 009 themes and accessibility | The logotype's two halves are aliases of the entity palette, so the contrast helper resolves one level of `var()` the way the cascade would, and both halves are held to AA on the header surface in every theme. Contrast ratios are computed from the stylesheet by the WCAG 2.1 formula and asserted for every text pair the interface uses, **in every theme** rather than only in the one named for contrast - two themes were below AA until that test was written, because looking at a colour is not a way of measuring it. The high-contrast theme additionally has its borders held to 3:1. Entity tints are recomputed in Go at each mix level the stylesheet uses, for every colour in every theme, so colouring a whole row cannot make the text on it unreadable. The contrast maths is itself verified against known reference points (black on white is exactly 21:1, `#767676` on white is the 4.5:1 boundary). Structural checks cover the skip link's position and target, the landmarks and their names, `aria-current` on the current page, an accessible name on every form control, `aria-live` on the totals, and text alternatives wherever colour or an icon carries meaning. None of this replaces the manual screen-reader pass in §5. |
| 010 durability | Migrations apply from empty and from each released schema version; a tampered checksum fails startup; a newer database version fails startup; backup → wipe → restore reproduces database *and* attachments, with the receipt bytes compared rather than counted. Archive tests cover the round trip, that an encrypted archive does not contain its plaintext, that several wrong passwords are all refused, that a flipped bit is detected, and that restoring twice does not double the week or duplicate a receipt. Two guard the interoperability a round trip cannot see: the AE-2 header fields other archivers key on - method 99, zero CRC, no data descriptor, the encrypted flag bit, the 0x9901 extra field - and a known-answer vector pinning the counter as little-endian, since decrypting our own output would work equally well with it reversed. |
| 011 readability | `gofmt -l` empty and `go vet` clean, both enforced by `make check`. `make lint` runs golangci-lint when it is installed and prints a note and continues when it is not, so "linter clean" is a property of the machine the command was run on rather than of the repository. There is now a doc-comment check: every package under `internal/` and `cmd/` says what it is for, and every exported function, type, constant and variable has a doc comment - with a member of a const or var block counting as documented when the block explains the set or the line carries a trailing comment, which is how the roles and the export formats are written. Methods on unexported types are exempt, since holding one means being inside the package already. Writing the check turned up thirty-one undocumented exported symbols, all of which now have comments. It is a floor rather than a standard: `// Foo does foo.` satisfies it. **Not covered:** there is no linter configuration file, and the default linter set does not include one. |
| 012 performance | Against 100k entries seeded across three years: day view, week view and timer start/stop under 100 ms p95; a one-year report under 2 s. Measured through the HTTP handler rather than against the store, because the requirement's rationale is that it validates the server-rendered choice - a store benchmark would leave template rendering unmeasured. Every figure is logged whether or not it passes, so a run is a trend as well as a gate. Tagged `perf` and excluded from `make check` because it takes about a minute; `make test-perf` runs it. The fixture forces the reminder window open, so the day and week views are measured doing their most expensive work rather than the cheap version they do before four in the afternoon - a budget that passes or fails by the clock is not a budget. A three-year export additionally has its *memory* asserted, not its speed: peak heap is sampled while the export runs and held under 64 MB, which 40 MB of JSON output meets at about 9 MB and the same export buffered does not, at 294 MB. That assertion was checked by restoring the buffering and watching it fire. The suite found all three interactive budgets breached by three to four times on first writing, and the causes are recorded in [ADR-0032](adr/0032-measured-before-tuned.md). |
| 013 attachments | Upload/paste round trip; deduplication of identical content; a user without access to the owning record gets 404, not 403; type sniffing rejects a renamed executable; oversize rejection; orphan sweep. SVG is stored rather than refused since ADR-0031, and the store test now pins what replaced the refusal: the file must be labelled `image/svg+xml` rather than the `text/plain` Go's sniffer returns, because every later decision keys on that type - and the detection is asserted narrow, so a note that merely mentions `<svg>` is not promoted to an image. Formats the standard sniffer misses (TIFF both byte orders, OLE documents) have their own test, because each was on the accepted list and unreachable. Preview tests cover the element chosen per type, TIFF transcoding and its pixel bound, DOCX text extraction including elements from other namespaces that share the name `t`, and the stripping of bidirectional overrides from a text preview. |
| 016 localisation | A catalogue parity test fails the build when any language lacks a key. Every screen is rendered in every catalogued language and scanned for leaked keys and for fmt's own complaints - a message given the wrong number of arguments does not fail, it renders `%!(EXTRA int=12)` into the page, and this test's comment claimed to catch that long before it did. The clock is fixed at an evening so the end-of-day panel is among the screens scanned, which is where the first such bug was. A separate test asserts no catalogue value holds an escape that was never decoded, since a double-escaped em dash is a valid string, present under the right key, that renders as six characters to a user. Formatter tests pin Swedish decimals, group separators, durations and money against English. Negotiation tests cover quality values, region variants and an unsupported header. |
| 017 help | Every help screen renders in every language. The help control is asserted to be a real link that returns a whole page without JavaScript. An unknown screen falls back rather than erroring. The markup renderer is tested against script injection, since help text is written by translators and its output is marked as trusted HTML. |
| 015 least privilege | Config tests assert that server mode refuses a public bind without TLS, that a half-configured TLS pair is rejected, that a group-readable private key is refused with a message saying how to fix it, and that no CBC or non-forward-secret cipher suite is offered. The platform profiles in `deploy/` are **not** covered by automated tests - they are verified by `scripts/harden-check.sh` and `systemd-analyze security` against a real deployment, which is stated plainly rather than implied. |
| 018 recoverability | `TestBackupRoundTrip` takes a backup, wipes, restores and compares - receipt bytes included rather than counted. `TestRestoreIsIdempotent` proves restoring twice does not double a week or duplicate a receipt; `TestPartialBackupByDateRange` and `TestPartialBackupByCustomer` cover the partial forms; `TestRestoreRefusesANewerFormat` and `TestRestoreRefusesNonsense` cover what must be declined. The archive-level tests are described under 010. |
| 019 interoperability (import) | `TestCSVImportPreviewAndCommit` walks the two passes; `TestCSVImportIsAllOrNothing` proves a bad row leaves nothing behind, which is the property that makes a preview worth having; `TestCSVImportRejectsAmbiguousDates` refuses a date that could be read two ways rather than guessing; `TestCSVImportCreatesMissingCatalogue` and `TestCSVImportHandlesExcelBOM` cover what a real spreadsheet arrives looking like. |
| 020 approval and correction | The lock is proved by removing it: a sanity run that neuters `checkPeriodOpen` must fail create, update, delete, timer start and the HTTP-level test together - a lock is only real if every mutation asks. Positive cases cover submit → withdraw → edit, edit refused *into* a locked week as well as inside one, an administrator being refused like anyone else, an empty week refused for submission, rejection requiring a reason, the reason reaching the owner's screen, a resubmission starting clean, nobody deciding on their own week in either mode, and reopen restoring the ability to edit. Week-start arithmetic is tested against Sunday-start weeks and a zone where late Sunday in UTC is already Monday locally. |
| 021 contract terms | Resolution is pinned as a table: which revision is in force on which day, including the day a revision starts and a revision agreed for the future. The project-over-customer merge is tested for the case that motivated it - a project that names only its overtime keeps following the account for everything else - and for the case that required the enumerations to gain an explicit default, where a project overrides a customer that does not pay for travel. A backdated entry is proved to price at the terms in force then, and moving it across a boundary to re-price. |
| 021 contract rules | The rate table is exercised for every kind against every combination of set and unset rule, because an unset rule silently behaving as zero would bill work at nothing - the most expensive failure available here. Quantity pricing is pinned exactly (42.5 km at 25.00 is 106250 minor units, not approximately that) and the formatter is proved to round-trip through the parser, so what is displayed can be typed back. Service tests prove the kind survives storage - it once did not, see §3a - that unbilled travel is still recorded in full, that a threshold produces a notice and marking the time stops it, that an explicit 0% markup is not overwritten by a customer default, and that a claim over the evidence threshold refuses the week's submission by name. |
| 022 learnability | Every guide topic is asserted to exist in every catalogued language, since a missing one renders as its own key - a page of gibberish rather than an error. The proxy topic is checked for the specific promises that make it worth having (a proposal, the inbox, the `@name` syntax, the shared project). Topics that cannot apply in the running mode are asserted absent. The markup renderer is tested for what it must and must not do: lists where every line is a marker, prose otherwise, and HTML from the catalogue escaped rather than executed. |
| 023 routines | The property the design rests on is proved by absence: having a routine creates nothing until it is applied. Weekday matching is tested across a whole week including Sunday, which is 0 in Go and 7 in the stored list. Applying all skips what is already recorded, and a routine cannot be applied by anybody but its owner. |
| 024 calendar import | The parser is tested against what Google and Outlook actually emit - folded lines with a space and with a tab, TZID parameters, DURATION instead of DTEND, bare line feeds, escaped separators - because those are the details that silently truncate a meeting name. The service tests are about judgement rather than parsing: every cancelled, declined, all-day and recurring event is accounted for by name; matching produces one candidate or none; a preview writes nothing; a re-import detects what is already there; a failure on one event does not fail the rest. |
| 025 search | Each mechanism is tested for the property that justifies it, not merely for returning rows: trigram finds a fragment inside a word, a two-character query falls back rather than returning nothing, a query containing FTS5 operators is matched literally, and a regular expression is a regular expression with a malformed one reported as the searcher's mistake. The index is proved to follow edits, tag renames and deletions. |
| 028 budgets and burn | The consumption arithmetic is pinned including the cases a naive version gets wrong: an overrun reports a percentage past 100 and a negative remainder rather than clamping either, and a project with both caps reports the worse of the two, since reporting the hours while the money is nearly gone is how a project overruns while its report looks calm. The projection is tested mostly through its refusals - no budget, already over, nothing recorded recently, fewer than two active weeks, and consumption in the unit that is not budgeted - because each would otherwise produce a confident date from no evidence. Averaging over active weeks rather than over the window is asserted by comparing a busy project against a steady one with the same total. Service tests cover what reaches the report from the database: a flagged entry is a question rather than consumption, a project with no budget does not appear at all, work from six months ago counts as used and not towards the rate, and a member gets no report rather than an empty one. The HTTP tests cover the form round trip, since a budget that saves without reappearing reads as one that did not save, and that an unreadable figure is a 400 rather than a silent zero - zero being a real value here that means the opposite of unset. |
| 027 reminders | The tests are about *when* a nudge is true, since too early is the failure that matters: a panel that is always there is one nobody reads, which silently disables the nudge that catches a timer left running overnight. The end-of-day window is proved local rather than server-side by asserting the same instant against Stockholm and Chicago, and an hour outside the day is asserted to clamp rather than to nudge always or never. The week window is a table across Wednesday, Friday morning, Friday afternoon, the weekend and the following Monday, plus a second case in a Sunday-start week - because a rule written as "Friday" is right by accident for the default and wrong for everybody else. Service tests cover the promises: an empty week is never nudged about, a dismissal is scoped to its day and tomorrow asks again, dismissing twice is one row rather than an error, recording the time makes the nudge stop being computed with nothing to clear, and a submitted week ends the prompt. The HTTP tests cover what only the boundary can get wrong - the panel appears on the day and week screens and nowhere else, never for a day or week other than the current one, the running-timer nudge carries the control that fixes it, and a dismissal with an unknown kind or a scope that is not a date is a 400 rather than a row nothing will ever match again. |
| 026 idle time | The arithmetic is pinned as intervals rather than as durations, because a resolution that gets the total right and the interval wrong shows up on the timeline instead of in the numbers: keep, discard and split are each checked against an entry with the observed stretch leading, interior and trailing. One property test asserts that whatever the answer, what is kept plus what is removed is the entry - a resolution that loses a minute to arithmetic makes a timesheet stop adding up to the day. The refusals are tested as carefully as the answers: a running timer cannot be resolved (its interval is still being measured), a stretch covering the whole entry offers no answer that would empty the row, and a stretch reported twice is one question. Service tests prove the rules that make the feature safe rather than merely working - a submitted week refuses a discard, a colleague can neither file nor answer an observation about somebody else's entry and cannot see one, and switching the feature off stops it storing anything. The HTTP tests cover what a browser may claim: a report that will not parse is a 400 rather than a silent 204, an unknown resolution is refused rather than treated as one of the three, and the two invisible attributes the watcher needs - the threshold on the body, the entry id on each running timer - are asserted, since nothing but a test notices when one goes missing. |
| 014 exactness | Rounding boundary tables (exactly on the increment, one second either side, zero, negative guard); rate × duration half-away-from-zero; mixed-currency addition refused. Property tests hold the rules that a table cannot state: rounding never moves a duration by a whole increment, always lands on a multiple of one, never rounds the wrong way, and is idempotent for the increment - which it is not once a minimum is configured, and that is why rounding happens exactly once, from the raw duration, at one call site. `ParseDuration` is the one function allowed a float, and every tenth and hundredth of an hour from 0.01 to 24 is checked against the integer answer, because 7.7 hours is 27719.999… seconds and a truncating version would bill an hour of work a second short. Money round-trips through its decimal form for the values that break naive formatters (a negative under one major unit, an amount of five minor units), and parses what people actually type: a comma for a point, ordinary and non-breaking spaces as thousands separators. Four source-level rules in `internal/repocheck` now hold the representation the arithmetic rests on: no struct field in `internal/domain` or `internal/store` is a float; `internal/store` does not mention `float64` or `float32` anywhere, stated as a bright line because nothing a persistence layer does needs a fraction; no value anywhere named for its unit - anything ending in `Minor`, `Seconds`, `Cents` or `Millis` - is a float; and nothing outside the stated exception parses one from text. The exception is `domain.ParseDuration`, which reads the decimal-hours shorthand people type ("1.5") and rounds it to whole seconds in the same expression; it is named in a list that fails if it stops parsing anything. Floats used for what floats are for - a PDF's coordinates, an icon's anti-aliasing, a progress bar's width, an `Accept-Language` quality value - are untouched, which is why the rules key on the name and the layer rather than banning the type. |

### An open question the property tests turned up

Under a *nearest*-increment rule, a five-minute entry against a quarter-hour
increment bills nothing: nearest rounds it to zero. Under a *down* rule the same
entry bills a quarter of an hour, because that branch has an explicit floor whose
comment argues that work must not vanish from an invoice without anybody
noticing. Both are defensible on their own terms and they cannot both be the
intended answer to the same argument. `TestNearestRoundsShortEntriesToNothing`
pins the current behaviour and says so; changing it would change what is
invoiced, which is not a decision a test should make quietly. A configured
minimum is what answers it today.

## 3a. Regression tests

Every defect that reached a running build gets a test named for its symptom
rather than for the code it touches. Most live in
`internal/web/regression_test.go`; some sit beside the feature they belong to
instead, where the fixture that reproduces them already exists - the entry below
says where when it is not the regression file. The list is specific on purpose: a
row that says "fixed a bug in exports" is a row nobody can check.

| Symptom | Cause |
|---|---|
| a failed import left the earlier rows written | the import wrote row by row, each in its own transaction, so a failure part-way through was a partial import with a summary audit row claiming otherwise (`internal/service`) |
| deleting an attachment removed the file before the deletion was final | the bytes went first and the audit row last, so a failed audit left the file gone with nothing in the trail to say who deleted it (`internal/service`) |
| a failed audit write left the change committed | catalogue mutations wrote the record on one connection and the audit row on another, so an error was returned over a change that stuck (`internal/service`) |
| editing a customer deleted its internal notes | no screen renders the field, so the handler read an absent form value and wrote an empty string over it on every save (`internal/web`) |
| the login page had no favicon | the root icon aliases were not on the public path list, so a browser asking for `/favicon.ico` before signing in got a redirect to the login page (`internal/web`) |
| `--addr=:8420` in local mode bound every interface | the loopback check answered yes to an empty host, so an unauthenticated instance was reachable from the network (`internal/config`) |
| a host named `127.0.0.1.example.com` counted as loopback | the check was a string prefix rather than a parsed address, so a name somebody else can register looked local (`internal/config`) |
| every account came back with an empty `PasswordSetAt` | the column was written by two statements and selected by none, so a rule about password age would have read blank for everybody (`internal/store`) |
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
| a duration corrected on the edit screen sprang back | the form's end time was applied after the duration, so whichever was not touched won |
| the expenses screen returned 500 with any row on it | a template reached for a field the listing does not populate |
| the day pane called a full day empty | the timeline was built from the wrong bound, so a day that filled the window rendered as nothing |
| every export was silently truncated at a thousand rows | the screen's row cap lived in the shared filter, so it travelled into the export - oldest first, in a file somebody was about to invoice from *(internal/web/pagination_test.go)* |
| a one-year export returned 500 | the tag lookup passed more bound parameters than SQLite accepts, which had never been reachable while something else capped the row count *(internal/store)* |
| the entries list said "Showing 151-137 of 137" | the count was rendered outside the branch that knew whether there were any rows |
| a client could sign in and was then refused every screen | the query resolving the acting user on every request did not select the column that scopes a client to their customer *(internal/service/client_test.go, internal/web/client_test.go)* |
| a client could download a backup containing the customer's rate | the check asked whether they may view their customer's time, which they may; a backup is not a report *(internal/web/client_test.go)* |
| a reminder read "1 timer is still running%!(EXTRA int=1)" | the plural helper always passes the count, and the singular had no verb to spend it on - caught by scanning rendered pages for fmt's own complaints *(internal/web/i18n_test.go)* |
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
service layer that enforces authorisation and audit, the store, the exporters,
the blob store, the authentication code and the configuration. There is
deliberately **no global percentage target**: chasing one produces tests for
trivial accessors while an authorisation branch goes unexercised. The floors sit
below the current figures rather than at them, so ordinary work does not fail the
build - the gap is a few points on most packages and closer to ten on `service`
and `blob`, which means a real regression there can pass. Lowering one is
expected to come with a note saying why.

Three packages moved a long way when they were finally looked at, and each move
found something:

* `auth` 38% → 86%. The whole OIDC flow was untested - every rule in
  `validateIDToken`, which is the difference between an ID token and an
  unverified assertion by whoever sent it. It is now exercised against a
  provider the test stands up and signs tokens with, including the tokens an
  attacker would mint: `alg: none`, HS256, a forged signature under a real key
  id, a token for another client of the same provider, an expired one, one
  replayed from a different login.
* `config` 14% → 93%. It had been excluded as "mostly flag declarations". It
  decides what the process listens on: the tests found `:8420` and
  `127.0.0.1.example.com` both treated as loopback, either of which puts an
  unauthenticated instance on the network.
* `store` 34% → 50%. Sessions, accounts, attachments, timesheet periods and
  search had no store-level tests at all, which is where the SQL semantics live -
  what a delete matches, what the prune sweeps, which columns a read selects.
* `domain` 72% → 87%, mostly through property tests rather than more examples.
* `web` 51% → 57%. The middleware chain runs on every request and had almost no
  tests of its own: which address reaches the audit trail, whether a forwarded
  protocol is believed, when HSTS is sent, that a panic cannot put a stack trace
  in a response. One test walks the route table out of `routes.go` and asks each
  route what an anonymous caller gets, so a new route that quietly becomes public
  fails the build. The catalogue handlers - edit, archive and restore, favourite,
  rename a tag, move time, correct an expense - were untested and turned up the
  notes defect above.

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
* **The idle watcher**, in a real browser: that a stretch with no interaction is
  reported after the configured threshold and not before, and that a clock jump
  is reported as the machine having stopped rather than as inactivity. Verified
  by leaving a real Chromium alone past the threshold, and by moving the page's
  clock forward; neither is reproducible in `httptest`, because both are about
  what a browser does when nobody is touching it.
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
make test              # unit, store, service, HTTP, golden, source scans, the binary; -race
make test-short        # skips store-heavy tests; the inner development loop
make coverage          # coverage profile + HTML report
make test-perf         # tagged performance suite against a generated dataset
make lint vet fmt      # static analysis and formatting

go test ./internal/export/ -update   # regenerate the golden files, then read the diff
make check             # everything CI runs
```

## 7. Coverage

Coverage is a diagnostic, not a target. We do not chase a global percentage; we do
require that `internal/domain`, `internal/service` and the authorisation code are
comprehensively covered, and that every branch of an authorisation or audit decision
is exercised. A pull request that lowers coverage in those packages needs a reason.

## 8. Continuous integration

**There is none.** No workflow is configured, on any host, so nothing in this
document runs automatically on a push. Everything here is a local `make` target,
and the gate is somebody typing `make check`.

That is worth stating plainly because this section previously described a CI
setup in the present tense - build matrix, race detector, golden-file diff,
vulnerability scan, a scheduled performance run - none of which existed. A
document that describes an imagined pipeline is worse than one that admits to
having no pipeline: it is exactly the assurance somebody would rely on instead
of checking.

What `make check` does run, on the machine it is run on: `gofmt -l`, `go vet`,
golangci-lint if it happens to be installed, a cross-compile of every package for
all three OSes and both architectures, the full suite (with `-race` when a C
compiler is available, without it otherwise), and the coverage floors. The
performance suite is excluded and is `make test-perf`. `make vulncheck` exists
and is **not** part of `make check`.

Setting CI up is a small job and would make the OS matrix an execution matrix
rather than a compile check - which is the one thing local `make check` cannot
be. Until it exists, ASR-002's proof is weaker than its fit criterion asks for,
and §3 says so in the row rather than here.
