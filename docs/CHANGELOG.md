# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Architecturally significant changes reference the [ADR](adr/README.md) that records
the decision.

## [Unreleased]

### Added

* **Budgets and burn** ([ADR-0035](adr/0035-burn-is-a-projection.md), ASR-028) —
  the last item outstanding from layer 5. A project could carry a cap in hours
  and one in money since the very first migration; nothing set them and nothing
  read them. Both are now editable on a project, and a report shows what each
  engagement has used, worst first, with a weekly rate and a projected date.
  The projection is the part that needed deciding rather than implementing. It
  is an average over a stated four-week window, taken across the weeks that had
  work so a fortnight's holiday does not double the apparent runway, and it
  refuses outright in four cases — no budget, already over, nothing recorded
  recently, fewer than two active weeks — each of which says so in the cell,
  because a blank in a "runs out" column reads as *never*. An overrun is shown
  as a percentage past 100 and a negative remainder rather than clamped: a row
  capped at 100% looks exactly like a project that landed on budget.

* **End-of-day and end-of-week reminders**
  ([ADR-0034](adr/0034-reminders-are-shown-not-sent.md), ASR-027) — the last item
  outstanding from layer 6. Four nudges: a timer still running, a day with
  nothing recorded, time a colleague recorded for you, and a week nobody has
  submitted. Each is *computed when a screen is drawn* rather than sent, which
  is the whole design: there is no scheduler, no mail, no queue and no new
  dependency, and a nudge cannot arrive about a timer that was stopped a minute
  ago — recording the time makes it untrue and it simply stops being computed.
  They wait for a configurable hour of the person's own local afternoon, so an
  instance serving two time zones nudges each at the end of their own day; the
  week nudge waits for the last working day of the configured week, since every
  week is unsubmitted on a Wednesday and a panel that is always there is one
  nobody reads. An empty week is not nudged about at all — that was somebody's
  holiday. Each can be dismissed for its day or week and returns the next one.
  Instance-wide switch in Settings.

* **Time the application saw nothing during is reported, never removed**
  ([ADR-0033](adr/0033-idle-time-is-observed.md), ASR-026). A timer left running
  through lunch was the one common mistake nothing caught: the overnight case is
  flagged by the timer limit, but six hours is a plausible morning. While a timer
  runs, the page watches for two things it can honestly see — that it stopped
  running (the machine asleep, or the tab suspended) and that it ran untouched —
  and reports either as an observation. Once the timer stops, the Today screen
  asks about it, with *keep*, *split* and *discard*, each showing what it would
  leave on the timesheet. Nothing happens without somebody pressing one, keep is
  the default, and the wording says what was seen rather than concluding that
  anybody was away — because a page cannot know that, and a tracker that
  subtracts hours on that evidence removes work from a file somebody is about to
  invoice from. Answering is an edit, so it respects the week lock, the
  authorisation rules and the audit trail; nobody, administrator included, can
  file or answer an observation about somebody else's time. Configurable in
  Settings, with a switch to turn it off.

* **Exports stream instead of being assembled.** CSV and JSON are now written
  row by row from a database cursor, so the memory a download costs is the size
  of a row rather than the size of the file. A three-year export of the
  100,000-entry performance dataset produces 17.3 MB of CSV and 39.9 MB of JSON
  at a peak heap of about 9 MB; the same two exports built in memory peaked at
  208 MB and 294 MB, and were slower. The peak is asserted in the performance
  suite, because a memory bound nobody measures is a memory bound that comes
  back. PDF, DOCX and Markdown cannot stream — each needs the whole report to
  paginate, size a table or compute a total — so instead of failing at whatever
  size the machine happens to run out at, they count the rows first and refuse
  above 50,000 with a message naming the two formats that have no limit.
  A streamed response cannot change its mind about its status code once the
  first byte is out, so the first row is pulled before any header is set: a
  malformed regular expression is now a 400 saying what to fix, where it had
  been a 200 CSV containing its header row and nothing else.

* **The Entries list is paged**, fifty rows at a time, with the total beside it
  ("Showing 51–100 of 137 entries") and a pager that carries the whole filter on
  every link. Plain links, so it works with no script; the current page is marked
  `aria-current`, and the window of page numbers is bounded so a filter matching
  ten thousand entries does not render two hundred links. A page past the end
  says so rather than looking like an empty database.
* **A full icon set, and a web app manifest.** The application had one 32-pixel
  SVG favicon and nothing else: no home-screen icon, no manifest, and a 404 on
  `/favicon.ico` for every page load. There are now tab icons (SVG, 16/32/48 PNG
  and a multi-size `.ico`), an opaque 180-pixel `apple-touch-icon` for iOS, and
  192/512 icons in a manifest with *maskable* variants for Android's adaptive
  shapes — so the application can be added to a home screen and launches
  standalone. They are drawn by a checked-in Go generator from the geometry in
  `favicon.svg`, so changing the mark is one shape and `go generate`, not eight
  files re-exported by hand. Every platform rule that is invisible until somebody
  installs the thing — opacity, declared sizes, the maskable safe zone, an
  installable manifest — is a test.
* **The logotype is the name in two colours**, Time in red and Tracker in blue,
  beside a larger clock mark. The halves are catalogue entries rather than a
  substring taken at a fixed offset, and carry no whitespace between them, so the
  name is still one word to a screen reader. Neither colour has a value of its
  own: both are aliases of the entity palette, so every theme's own red and blue
  carry the brand, and both are held to WCAG AA on the header in all eight.

* **PDF, DOCX and Markdown on the Entries screen.** PDF and DOCX had been
  written and tested since layer 5; the screen offered CSV and JSON under a hint
  saying the other two arrived later. All five now come from one list in the
  export package that both the screen and the router read, so a format cannot be
  offered that cannot be produced. Markdown is new, for the case the others do
  not cover — pasting a week into a ticket, a wiki or an email — and its tables
  are padded so the output is legible as source too.
* **Backups are zip archives carrying their attachments**, optionally encrypted
  with AES-256 in the WinZip AE-2 scheme using a password set in Settings
  ([ADR-0030](adr/0030-encrypted-backup-archives.md)). ADR-0013 always said a
  backup with a timesheet and no receipts is not a backup; it is now true. The
  format is the one 7-Zip, WinZip and Keka already read, because an archive only
  this binary can open is not a backup. Restore accepts an encrypted zip, a
  plain zip, or a bare JSON document from before archives existed.
* **Attachments are previewed in place** — PNG, JPEG, GIF, WEBP, BMP, SVG, TIFF,
  PDF, and the text of a Word document
  ([ADR-0031](adr/0031-attachment-previews.md)). There had been no screen listing
  attachments at all, so the download and delete routes were unreachable. TIFF is
  transcoded to PNG because no browser but Safari renders one; a DOCX yields its
  text, labelled an extract rather than a preview.
* **SVG can be attached.** It was refused outright as script-capable. It is
  accepted now because the preview route makes it inert: served inside an `<img>`,
  where no browser runs script, behind a response policy of `default-src 'none';
  sandbox`. Verified against a hostile SVG in a real browser, both in the page and
  navigated to directly.
* **Every entry row carries its date and weekday**, and the Entries filter gains
  project, assignment, and time recorded on a day its project also had an expense.
  A "clear filter" link appears once anything is narrowed.
* **Where the navigation sits** is now a setting: across the top or down the left.
  One attribute on `<html>` and no markup change, so the reading order and the tab
  order are identical either way. Below 900px the rail becomes the top bar again.
* **Clock and date formats** are settings: 24- or 12-hour, and ISO, day-first or
  month-first dates. Both default to following the interface language.
* **The day pane's hours are configurable**, along with what happens to time
  outside them: grow the pane to cover it, or keep the hours fixed and report what
  fell outside above and below the pane. The pane was hard-coded to 08:00–18:00
  and always grew, which suits somebody who works late once a month and squeezes
  the ordinary day of somebody whose evenings are routinely busy.

### Fixed

* **Exports were silently truncated at 1000 entries.** The Entries screen capped
  its rows at a thousand, that cap was part of the filter, and the export handler
  built its download from the same filter — so any range with more entries than
  the cap lost the rest, oldest first, in a file somebody was about to invoice
  from. Worse, the amount lost depended on which page of the screen they were
  looking at. An export now covers everything the filter matches, whatever the
  screen is showing.
* **A tag lookup could exceed SQLite's bound-parameter limit.** Every entry id in
  the lookup is a parameter, and the whole statement is rejected past the
  ceiling — so the symptom is a 500 on a download, not a missing tag. It had been
  hidden by the export truncation above: the screen's thousand-row cap was, by
  accident, a cap on the query. The lookup batches now.
* **Every screen took about 300 ms against a realistic database**, where the
  budget is 100. Three queries on the page shell each walked every entry the user
  had, and each had an index that should have prevented it
  ([ADR-0032](adr/0032-measured-before-tuned.md)): a partial index the planner
  declined in favour of a broader one, a predicate that did not match the partial
  index it was written for, and `date()` wrapped around an indexed column, which
  makes a condition no index can answer. The day view is now 27 ms, the week
  view 32 ms, timer start/stop 28 ms — down from 365, 350 and 303.
  `ANALYZE` was tried and rejected: it made one query 230× faster and another 3×
  slower in the same run.
* **The inbox badge built the inbox.** A number shown on every screen was
  fetching every pending entry with its assignment, project, customer, both users
  and an attachment count. It is a count query now.
* **`make test-perf` passed with nothing behind it.** The target ran
  `-run TestPerf` and no test of that name existed, so it exited 0 in silence
  while TEST.md claimed ASR-012 was proved by it. The suite now exists, measures
  through the HTTP handler, logs every figure, and asserts the budgets.
* **An encrypted archive was structurally perfect and no other archiver could
  open it.** The general-purpose "encrypted" flag bit was never set, so every
  real tool handed the ciphertext straight to its inflater and failed with a
  decompression error. Nothing testable against our own reader could see it —
  found by opening an archive in another implementation, which is now a release
  step rather than a nicety.
* **No TIFF could ever be uploaded.** `image/tiff` was on the accepted list, but
  Go's content sniffer implements the WHATWG list and has no TIFF rule, so a TIFF
  arrived as `application/octet-stream` — which the same list explicitly refuses.
  Pre-2007 `.doc` and `.xls` were unreachable for the same reason. The allow-list
  said one thing and the code did another.
* **Every attachment in a backup lost its file extension.** A guard meant to keep
  path separators out of an archive entry name passed `".."` to `ContainsAny`,
  which matches the dot in every extension.
* **A downloaded export could cover more than the screen it was taken from.** The
  links were assembled by hand once per format and carried the customer but not
  the tags, the kind or the search query. They are built once now, from the same
  struct the screen was rendered with.
* **Expenses were backed up and never restored.** `RestoreResult` had a field for
  them and nothing filled it.
* **The day pane could claim a full day was empty.** With a fixed window and every
  entry outside it, the empty state was rendered without asking whether anything
  had been pushed out.

* **Two themes were below WCAG AA on text, and nothing was checking.** The
  contrast test only ever examined the theme named for contrast; the sand
  accent was 4.29:1 as link text and the spring accent 3.77:1 as link text and
  4.07:1 under the white text of a primary button. Both are corrected by
  darkening one token each, and every theme is now held to AA on text by test.
  Found while adding the terminal theme — looking at a colour is not a way of
  measuring it, which is the whole argument for computing this.
* **The light theme's colours were never actually tested.** `themeBlock` looked
  for a `[data-theme]` block and the default theme is declared on `:root`, so it
  returned nothing and the callers skipped it — one of them with a comment
  claiming it was covered elsewhere. It now knows about `:root`, and a theme
  that is offered but undefined is an error rather than a silent skip.
* **A migration wrote timestamps the application could not read.** The carry-over
  in 0007 used SQLite's `datetime()`, which separates the date and time with a
  space where every timestamp here is RFC 3339. No test on a fresh database
  could see it, because a fresh database has nothing to carry over. Migrations
  can now be applied up to a version so an upgrade with real rows in it is
  testable, and a content check rejects `datetime()` in any migration.
* **The header clock showed `--:--:--` and never updated.** Three defects, one
  symptom. The value was a placeholder for JavaScript to replace, so a script
  that never ran, one that failed before reaching it, and a broken clock were
  indistinguishable and all silent — the time is now rendered by the server, so
  the worst case is a stale clock rather than a visibly broken one, and it works
  with no script at all. It read the *browser's* zone while every other time on
  the page used the user's. And `init()` ran seven features in sequence with no
  isolation, so the first to throw silently disabled the six after it; the clock
  was second, which made almost any early fault produce exactly this symptom.
  Each feature now starts independently and a failure is logged.
* **The inbox returned 500 as soon as it held a proposal.** A custom template
  function named `index` shadowed the Go template builtin, so an ordinary map
  lookup failed with "wrong type for value; expected []int64" — an error that
  says nothing about the cause. The expenses screen would have done the same.
  Renamed to `nth`; both screens have regression tests that keep a row on the
  page, since the fault only appears when there is one.
* **The service layer kept its own copy of the time-entry insert and update.**
  The copies had already drifted — the transactional update never wrote
  `time_zone` — and a newly added column was written by one and not the other,
  so a field the application believed it was storing was not stored at all.
  There is now one statement each, in the store, with the service calling the
  transactional form.
* **Editing an entry was implemented but unreachable.** The service, the routes
  and the form all existed; no screen ever rendered a link to any of them, so
  the only way in was to type a URL. Rows on the day and entries screens now
  carry an edit control, and a regression test asserts the link rather than the
  handler - the handler was never the broken part.
* **A completed form was rejected as empty.** Creating a customer through the
  browser failed with "customer name is required" even with every field filled
  in. `fetch(FormData)` sends `multipart/form-data`; `r.ParseForm` does not parse
  a multipart body but *does* set `r.Form`, so the later `r.FormValue` never fell
  back to the multipart parser and every field arrived empty. Form decoding now
  goes through one helper that picks the parser from the content type, and the
  client sends URL-encoded bodies so the JavaScript and no-JavaScript paths are
  handled by identical code. Covered by a regression test that submits the same
  form in both encodings.

### Added

* **A terminal theme**: green phosphor on near-black, monospace throughout,
  square corners, a faint glow and static scan lines, and a blinking block
  cursor on the running-timer clock — the one genuinely live thing on the
  screen, which is where a terminal would have put it. It is the only theme
  that changes the typeface, deliberately: green-on-black in a proportional
  face is a dark theme with an unusual accent, not a terminal. The glow and
  scan lines are removed under `prefers-contrast: more` and
  `prefers-reduced-transparency`, and the cursor stops blinking under
  `prefers-reduced-motion`. The entity palette stays a palette — a phosphor
  tube could not have shown seven colours, but a timesheet that cannot tell one
  customer from another is a worse anachronism than a colour CRT.
* **Contract terms are dated, and attach to a project as well as a customer.**
  ADR-0024 named two costs it was accepting — no history, one set of terms per
  customer — and both have now been asked for. A revision records when it takes
  effect and why; the terms that price an entry are the latest in force on the
  day that entry belongs to, with a project's laid over its customer's field by
  field. A project that differs only in overtime says only that. Backdating an
  entry into an earlier contract period now prices it at that period's
  agreement. See [ADR-0026](adr/0026-dated-contract-terms.md).
* **Tags.** The tables have existed since the first migration and never held a
  row: quick-add parsed `#travel` out of the line and threw it away, which made
  the sigil a way of deleting a word from your own note. Tags now persist,
  normalise (`#Travel` and `travel` are one tag), filter, appear on rows linking
  to themselves, and reach the exports. They are created by use — a labelling
  system that has to be set up first does not get used.
* **Search**, over the note, assignment, project, customer and tags. Three
  mechanisms, chosen per query and named on screen: a trigram full-text index
  for ordinary substring search (`redir` finds `login redirect`, which a
  word-boundary index cannot); a scan for queries too short to trigram, which
  the index would answer with silence rather than everything; and regular
  expressions on request, RE2 so a pathological pattern cannot hang the process.
  The query is always literal — a user typing `C++` means those characters.
  See [ADR-0029](adr/0029-searching-with-trigram-and-regexp.md).
* **Copy a day or a week.** Times of day, tags and kinds come across, so a
  copied day looks like the day it came from; a week copy aligns day for day.
  Amounts are priced afresh, because the target may fall in a different contract
  period. Running timers are skipped — one has no length yet.
* **Routines**: lunch, the stand-up, Friday admin. A template, not a scheduler.
  The day view offers what is due and a person clicks it; nothing is created by
  the passage of time, because an entry created because the calendar said
  Tuesday is an hour nobody worked and it reaches an invoice looking exactly
  like the forty around it. See
  [ADR-0027](adr/0027-routines-are-offered-not-fired.md).
* **Calendar import** from Outlook, Google Calendar or any iCalendar export.
  The format is the easy half; a calendar also contains cancelled meetings,
  declined meetings, public holidays and blocks somebody made to protect an
  afternoon. Every event is listed in a preview that writes nothing, with the
  ones that are not work shown as such **by name** — a count is not enough,
  because somebody has to be able to see that the missing hour was a meeting
  they declined. Matching produces one candidate or none, never a guess.
  See [ADR-0028](adr/0028-calendar-import-is-a-conversation.md).
* **A day timeline** with drag to move and drag-the-edge to resize. Overlapping
  work sits side by side in lanes rather than one block hiding another, which is
  the whole reason it is worth drawing in an application that allows concurrent
  timers. Positions are CSS grid slots chosen by the server, not inline styles:
  the content security policy forbids those, and weakening it for geometry would
  weaken it for everything. Every block also carries a plain time-and-length
  form, so the whole view works with no JavaScript at all.
* **One-click starts** ordered by favourites first, then what has been used most
  in the last six weeks — frequency before recency, because a list that
  reshuffles after every entry is one nobody builds muscle memory for.
* **"Switch to X"**, which stops what is running and starts one thing in a
  single action. Concurrent timers remain the model for genuinely parallel work;
  this is the one-click version of "and now instead", which as stop-then-start
  leaves a gap if somebody is interrupted between the two clicks.
* **Customer contract terms: overtime, travel time and reimbursement.** Overtime
  and travel are a *kind* on the entry, chosen by the person; the customer's
  rules say what each is worth. Nothing is reclassified automatically — whether
  an hour counts as overtime is a contractual judgement, and a threshold that is
  exceeded produces a notice on the week view rather than a rate nobody agreed
  to. Travel a customer does not pay for is its own state, not a zero rate: the
  time is recorded in full and carries no amount. Reimbursement covers a default
  markup, mileage per kilometre and per diem per day — a quantity is priced from
  the customer's rate, with the working shown on the claim — and a receipt
  threshold enforced when the week is submitted. The kind reaches CSV, JSON, PDF
  and DOCX, because a line billed at one and a half times the base rate has to
  say why on the document carrying the figure. See
  [ADR-0024](adr/0024-customer-rate-rules.md).
* **An approval status report**, per person per week. The queue answers "what is
  waiting for me"; this answers "who has not submitted", which no queue can show
  because a submission that was never made has no row. Weeks with time recorded
  and nothing handed in are marked; weeks nobody worked stay blank, so the cells
  that matter are not buried. Scoped like everything else: without the manage
  permission it is your own weeks.
* **A task-oriented guide** at `/guide`, alongside the per-screen help. Ten
  how-tos in numbered steps — recording time, fixing an entry, moving time,
  **recording time for a colleague**, submitting a week, approving one, expenses,
  exporting, backup, and setting up customers and contract terms. It exists
  because per-screen help cannot reach somebody who does not yet know which
  screen they need, which is exactly the position of anyone asked to put a
  colleague's hours in. Catalogue content, so translated and escaped like the
  rest; topics that cannot apply in the running mode are not offered. See
  [ADR-0025](adr/0025-task-oriented-guide.md).
* **Weekly submit and approve.** A person declares a week finished; a manager
  accepts it or sends it back with a reason; an approved week can be reopened,
  deliberately and audibly. Submitting locks the week - creating, editing,
  moving or deleting time in it is refused, as is starting a timer inside it or
  stopping one into it - and **an administrator is not exempt**, because a lock
  the most privileged user walks through is not a lock. Nobody decides on their
  own timesheet whatever their role, and the single-user build refuses the
  action outright rather than offering a control that means nothing. Enforcement
  is one function that every mutation calls; the tests prove that disabling it
  fails every one of those paths. See
  [ADR-0023](adr/0023-week-as-the-unit-of-approval.md).
* **Correcting an entry that is already recorded.** A screen of its own for the
  mistake this exists to fix: eight minutes typed where eight hours were meant.
  The duration is shown in the notation it accepts (`8m`, `8h`, `1h 30m`) beside
  the day and the start time, so a wrong value is legible as wrong. The end-time
  field is deliberately left empty: an end time takes precedence over a
  duration, so a prefilled one would silently discard the correction just typed.
  Every change is audited with the previous value.
* **PDF and DOCX export**, both generated in-process. The PDF writer is
  hand-written against the standard 14 fonts; its cross-reference table is
  verified by an independent parse in the tests, which is what a strict reader
  checks. DOCX is OOXML written into a zip with the standard library.
* **Attachments** on time entries and expenses: a content-addressed blob store
  (identical files stored once), uploads size-capped and type-sniffed rather
  than trusted, SVG refused as script-capable, the extension required to agree
  with the content, and every byte served through an authorising handler.
  Pasting an image from the clipboard attaches it, through the same endpoint and
  the same checks as a file input.
* **Expenses**, keeping billable and reimbursable as independent questions
  throughout, with markup on the billable side only and the billed amount frozen
  when recorded.
* **Proxy entries**, completing the consent workflow: time proposed for a
  colleague counts for nothing until they accept it, only the subject can
  decide, a rejection is kept with its reason, and overlapping proposals are
  flagged.
* **CSV import of hours** — previews every row with its problems and writes
  nothing, then imports all or none ([ADR-0022](adr/0022-two-pass-csv-import.md)).
  Ambiguous dates are refused rather than guessed.
* **Backup and restore** as a single JSON file, whole or partial by customer,
  project or date range, merging rather than replacing so a mistaken restore
  cannot lose data, with optional scheduled backups
  ([ADR-0021](adr/0021-json-backups-that-merge.md)).
* **A YAML configuration file**, found automatically or named with `--config`,
  plus `--verbose` and `--debug`.
* **Minimum 2h/4h/8h rounding presets**, alongside the increment rules.
* **Editable customers, projects and assignments**, one-click favourites, and
  **moving time between assignments** — across projects and customers — with the
  billing snapshot recomputed from the target and the move recorded in the audit
  trail.
* **Rate inheritance from the customer** when neither the assignment nor the
  project sets one.
* **Administrator toggles** for the header clock and the current date.
* **Regression tests** for every defect that reached a running build, and
  `make coverage-check` with per-package floors, wired into `make check`.

* **HTTPS, certificate scripts and platform hardening.**
  * TLS terminated by the application itself (`--tls-cert`, `--tls-key`), with a
    TLS 1.2 floor and 1.2 suites restricted to AEAD constructions with forward
    secrecy. An optional listener redirects plain HTTP with a 308. HSTS
    available but off by default, because it is hard to undo.
  * Server mode refuses to bind a non-loopback address without TLS, an upstream
    TLS declaration, or an explicit `--allow-insecure`. A group- or
    world-readable private key is refused with a message saying how to fix it.
  * `scripts/gen-cert.sh` and `gen-cert.ps1` generate ECDSA P-256 certificates
    with proper SANs and constrained key usage, self-signed or from a local CA.
  * **Landlock** self-sandboxing on Linux (`--hardening=auto|enforce`),
    implemented against the raw syscalls and trimmed to the kernel's ABI
    version. Off by default; the shipped systemd unit enables it.
  * `deploy/` carries a hardened systemd unit, an AppArmor profile, an SELinux
    policy module, a launchd job with a `sandbox-exec` profile, and a Windows
    service installer using a virtual account with a restricted token.
  * `scripts/harden-check.sh` reports what is actually active for a running
    instance.

* **Internationalisation, with Swedish.**
  * Embedded JSON message catalogues, resolved entirely on the server so the
    correct language and `lang` attribute are present in the first byte.
  * Localisation beyond strings: Swedish renders `1 234,50` and `1 tim 30 min`
    where English renders `1,234.50` and `1h 30m`, with locale-aware dates,
    weekday and month names, and money.
  * Language chosen from the user's stored preference, then `Accept-Language`
    with quality values parsed properly, then English.
  * A parity test fails the build when a catalogue lacks a key, and every screen
    is rendered in every language and scanned for leaked keys.

* **Accessibility.**
  * A skip link, labelled landmarks, `aria-current` on the current page, an
    accessible name on every form control, `aria-live` on the totals, and text
    alternatives wherever colour or an icon carries meaning.
  * The high-contrast theme is verified against WCAG 2.1 AA by computing real
    contrast ratios from the stylesheet — and the contrast maths is itself
    checked against known reference points.

* **Context-sensitive help.**
  * Each screen has its own help topics, translated like everything else,
    covering the behaviours that are deliberate but surprising: overlapping
    timers, the two totals, the quick-add parser's refusal to guess, frozen
    rates.
  * A real link, so it works without JavaScript; with script it loads into a
    panel that manages focus in both directions.

* **Server mode** (layer 2 of [MVP_PLAN.md](MVP_PLAN.md)). `--mode=server` now
  runs a real multi-user service.
  * **Local accounts** with Argon2id hashing (OWASP parameters, stored inside the
    hash so they can be raised later and upgraded transparently on next login),
    constant-time verification, and a uniform failure response so the login form
    cannot be used to discover which accounts exist.
  * **OIDC single sign-on** — Authorization Code with PKCE, `state` and `nonce`
    verified, ID token signature checked against the provider's JWKS with an
    RS256 allow-list, issuer, audience and expiry validated. Accounts link on the
    immutable `sub` claim, never on email.
  * **Server-side sessions** with an opaque cookie whose SHA-256 is what gets
    stored, `HttpOnly`/`Secure`/`SameSite=Lax`, host-only, idle *and* absolute
    lifetimes, and immediate revocation on sign-out, password change, role change
    or account disablement.
  * **CSRF protection** on every unsafe request, with the token bound to the
    session and carried both as a hidden field and a header, so the
    no-JavaScript path is protected too.
  * **Login rate limiting** per account *and* per source address, with capped
    exponential backoff.
  * **RBAC**: admin, manager, member and client, scoped by project membership,
    enforced at one point in the service layer and covered by an exhaustive
    role × action × scope test matrix.
  * **Scoped listing queries** ([ADR-0016](adr/0016-scoped-listing-queries.md)) —
    the store offers no unscoped list, and the empty scope renders as "match
    nothing", so a forgotten scope shows an empty screen rather than everyone's
    data.
  * **rsyslog forwarding** in RFC 5424 through a bounded non-blocking queue, so a
    dead collector can never block or fail a user's request; drops are counted.
  * **Per-person project rates**, the level between the assignment and the
    project in rate resolution.
  * **Users screen** for accounts, roles and project access, and a first-admin
    bootstrap that refuses once any account exists.

* **Working local-mode application** (layer 1 of [MVP_PLAN.md](MVP_PLAN.md)).
  `bin/timetracker` starts, migrates its own database, and serves the UI on
  loopback with no configuration.
  * **Domain layer** — `Money` as integer minor units with currency checking,
    duration parsing (`1.5`, `1h30`, `90m`, `1:30`), rounding rules applied at
    one documented point, entity types and validation, interval union
    arithmetic.
  * **Storage** — SQLite through the pure-Go driver with WAL, foreign keys and a
    single write connection; embedded forward-only migrations with checksum
    verification, a newer-schema guard and a pre-migration backup.
  * **Service layer** — the single enforcement point: every method consults the
    authoriser, every mutation writes its audit row inside the same transaction
    as the change.
  * **Concurrent timers** — any number may run at once; overlapping intervals
    are recorded and reported, never auto-split. Stopping is idempotent, and a
    timer past the configured maximum is flagged for review rather than billed.
  * **Totals** — summed and elapsed reported side by side wherever entries can
    overlap, with the overlap stated explicitly.
  * **Billing** — layered rate resolution (assignment → project → customer →
    instance default) with the resolved rate and applied rounding rule stored on
    the entry, so a later rate change cannot rewrite an invoiced amount.
  * **Screens** — Today (timeline, gaps, quick add, one-click start), Week
    (assignment × day grid), Entries (filterable, the basis of every export),
    Admin (customers, projects, assignments with colours and icons).
  * **Quick add** — `2h acme/migration fixed the login redirect #travel`, with
    ambiguity reported rather than guessed.
  * **Gap detection** — unaccounted stretches surfaced on the day view as
    prompts, never filled in automatically.
  * **Seven themes** — light, dark, gold, sand, spring, autumn and high
    contrast, as redefinitions of one semantic token set, applied server-side
    before first paint.
  * **Exports** — CSV (UTF-8 with BOM so Excel reads it correctly) and JSON
    against a versioned schema, both rendered from one `Report` value so they
    cannot disagree.
  * **Keyboard shortcuts**, live client-side timer clocks, and a background
    submit layer, all degrading to plain form posts without JavaScript.
* **Tests** — domain, store, service, HTTP and architecture suites, including a
  test that fails the build if `internal/web` ever imports `internal/store`, and
  one that checks every theme defines every semantic token.

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

### Known gaps

* **A backup carries attachment metadata but not the bytes.** The blobs live on
  disk and must be copied separately. Stated in the help text and in
  [ADR-0021](adr/0021-json-backups-that-merge.md); a zip container holding both
  is the obvious fix.
* **CSV import writes row by row rather than in one transaction.** The preview
  makes a mid-import failure vanishingly unlikely, but it is not impossible.
* **The PDF layout engine is minimal**: text, rules, tables and page breaks. No
  images, no embedded fonts. Its output is structurally verified but has not
  been opened in a viewer here — see the manual checks in
  [TEST.md](TEST.md) §5.
* **Weekly submit-and-approve** is still outstanding from layer 4, as are budget
  burn reporting and the narrowed `client` projection from layer 5.
* **TOTP and API tokens are designed for but not built.** Neither is
  load-bearing for a small team behind SSO, and both are additive.
* **PDF and DOCX export return 501.** Both arrive in layer 5. CSV and JSON are
  complete.
* **The HTMX library is not vendored yet.** The templates use HTMX's attribute
  vocabulary, and `static/js/app.js` implements the subset the application
  relies on (`hx-post`, `hx-confirm`, and the `HX-Request`/`HX-Refresh` header
  protocol). Dropping the upstream library in its place needs no template
  changes. Until then the tree contains no third-party JavaScript at all.
* Attachments, expenses, proxy entries, tags and approvals are designed for but
  not implemented; see [MVP_PLAN.md](MVP_PLAN.md).

[Unreleased]: https://github.com/rom/timetracker/commits/main
