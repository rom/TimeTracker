# Design

> **Scope.** What the application does and how it behaves for the person using it:
> domain concepts, screens, interactions, themes and the rules that govern billing.
> For structure and mechanism see [ARCHITECTURE.md](ARCHITECTURE.md); for rationale
> see the [ADRs](adr/README.md).

## 1. Design principles

1. **Recording reality beats tidy data.** The tool does not force the day into
   non-overlapping blocks, and it never invents numbers to make totals reconcile
   ([ADR-0004](adr/0004-concurrent-timers.md)).
2. **Manual first, assisted second.** Every automated suggestion is a *proposal* the
   user accepts. Nothing is logged on a user's behalf without a click.
3. **Consent is structural.** Time recorded in someone else's name does not count
   until they say so ([ADR-0005](adr/0005-proxy-time-entry.md)).
4. **The fast path is the keyboard.** Logging time is a chore; the interaction that
   matters is the one that takes two seconds.
5. **Numbers must be defensible.** Every figure on an invoice can be traced to the
   entries, the rate and the rounding rule that produced it.

## 2. Domain concepts

```
Customer            who is billed. Currency, colour, icon.
  └── Project       a body of work for that customer. Budget, rates, rounding rule.
        └── Assignment   the thing you actually start a timer on.
                         Name, code, colour, icon, billable default, rate override.
```

**Assignment** is deliberately the level a timer attaches to, because that is the
granularity people describe their day in ("I was on the migration", "I was on
support"). Attributes are user-facing and cosmetic *and* functional: colour and icon
make the day view scannable at a glance; the code appears in exports and invoices.

Supporting concepts:

* **Time entry** — an interval on an assignment, by a user, optionally entered by
  someone else. Carries a note, tags, attachments, billable flag and status.
* **Expense** — a cost with a date, project, amount and category. Independently
  flagged **billable** (re-charged to the client, optionally with markup) and/or
  **reimbursable** (owed back to the person who paid). These are different questions
  and the app never conflates them: a taxi paid by the employee and re-charged is
  both; a client-paid hotel is neither.
* **Tag** — a cross-cutting label ("travel", "warranty", "internal") independent of
  the customer hierarchy, used for filtering and reporting.
* **Timesheet period** — a week (configurable start day) that can be submitted,
  approved and locked.

## 3. Time entry

### Timers

A user may run **any number of timers at once**. The header shows every running
timer with a live client-side clock, an assignment colour chip and a stop control;
it is deliberately impossible to lose track of. Overlapping intervals are legal and
marked in the day view as information.

Safety rails, because "forgot to stop it" is the dominant failure:

* a timer past a configurable maximum (default 12 h) is flagged for review and
  excluded from totals until the user confirms or corrects it;
* **idle observation**: a stretch during which the application saw nothing is
  reported, with *keep*, *discard the idle time* or *split into a separate entry*
  ([ADR-0033](adr/0033-idle-time-is-observed.md)). What it can see is its own
  page: that the page stopped running - the machine asleep or the tab suspended -
  or that it ran untouched. Neither is a person being away, so neither is
  described as one, and none of the three answers happens without somebody
  choosing it. Every answer shows what it would leave behind, because discarding
  a break in the middle of a six-hour entry drops the afternoon too;
* stopping is idempotent — a double-click cannot produce a negative or doubled
  duration.

### Manual entry

The timesheet grid is a first-class way in, not an afterthought: pick a day, an
assignment and type a duration. Accepted duration formats: `1.5`, `1,5`, `1h30`,
`90m`, `1:30`. Start/end times can be entered instead of a duration.

### Quick add

A single input parsed into an entry:

```
2h acme/migration fixed the login redirect #travel
15m internal standup
9:00-10:30 acme/support @erik      ← proposes 90 min for Erik, pending his confirmation
```

Rules: a leading duration or time range sets the interval; `customer/assignment`
resolves fuzzily against recent entries; `#word` is a tag; `@person` makes it a
proxy proposal; the remainder is the note. Anything ambiguous opens the full form
pre-filled rather than guessing.

### Correcting an entry

An entry is a record of something that happened, and the record is frequently
wrong on first attempt. Correcting it is therefore a normal operation with a
screen of its own, not a hidden one.

The dominant error is a **duration in the wrong unit**: `8m` typed where `8h` was
meant. Everything about the correction screen is arranged around making that
visible.

* The entry is restated in words first — the day, the start time, and the
  duration as it currently stands — because a value sitting in an input reads as
  something to type over, and this one is the thing to check.
* The duration is shown in **the same notation it accepts**: `8m`, `1h 30m`,
  `8h`. What is on screen can be typed straight back. `0h 8m` where a working
  day should be is legible as wrong at a glance; `480` would not be.
* The field order is assignment, day, start, duration. The eye arrives at the
  duration having already confirmed everything around it.
* The **end time is left empty**. An end time takes precedence over a duration,
  so a prefilled one would silently discard the duration the user had just
  corrected — the exact failure the screen exists to prevent. Filling it in is a
  deliberate choice, and the field says what it does.

Every change is audited with the previous value, so a figure that has been
corrected can still be explained afterwards. If the week has been submitted or
approved, the correction is refused with a message naming the way to unlock it —
see §4a.

### Copy, paste and attachments

* Pasting into the note field accepts plain text; pasting an **image** (screenshot,
  photographed receipt) creates an attachment inline without leaving the page.
* Files can be dropped onto an entry or expense.
* Attachments are **previewed in place** — PNG, JPEG, GIF, WEBP, BMP, SVG, TIFF,
  PDF, and the text of a Word document
  ([ADR-0031](adr/0031-attachment-previews.md)). The question being asked of a
  receipt is almost always "is this the right one", and downloading it is a poor
  way to answer that. TIFF is transcoded to PNG because no browser but Safari
  renders one and office scanners still produce them; a DOCX yields its text, and
  is labelled an extract rather than a preview, because its layout and tables are
  not shown. Everything else is stored and downloadable.
* A copied entry can be pasted to duplicate it onto another day; a row of durations
  copied from a spreadsheet pastes into the week grid.
* "Duplicate yesterday" and per-user **favourite assignments** cover the repetitive
  case.

## 4. Colleague (proxy) time

Two people work together; one of them tracks time properly.

The tracker opens the colleague's row, records the time on the shared assignment,
and the entry lands in the colleague's **inbox** as *pending, entered by <name>*. It
counts for nothing until accepted. The colleague accepts, edits-then-accepts, or
rejects with a reason. Everything is audited, and the subject always sees who
proposed what.

Design details that matter in practice: pending proposals are surfaced in the
colleague's header and in reminders, since a proposal nobody looks at is unbilled
work; overlapping proposals from two different people for the same period are
flagged together rather than silently duplicated; and a rejected proposal stays
visible to its author, so the conversation happens in the tool.

## 4a. Submitting and approving a week

Hours that have been billed must stop moving; hours that have not must be easy to
fix. The week is where those two needs are reconciled
([ADR-0023](adr/0023-week-as-the-unit-of-approval.md)).

```
open ──submit──▶ submitted ──approve──▶ approved
  ▲                  │                      │
  └──withdraw────────┘                      │
  ◀──────────reject (with a reason)─────────┤
  ◀──────────reopen (deliberate)────────────┘
```

**Submitting locks the week to its owner.** No time in it can be created,
edited, moved or deleted; no timer can be started inside it or stopped into it.
That is what submitting means — hours that keep changing afterwards are not a
declaration.

**Withdrawing is the owner's own** as long as nobody has decided yet. People
submit and then remember something, and needing a manager to undo that makes
them submit late instead, which costs more than it protects.

**Only a manager approves, and never their own week**, whatever their role. The
single-user build has nobody else, so it refuses the action rather than offering
a control that means nothing.

**A rejection carries a reason**, and the reason is shown to the owner on the
week view — the screen where they will act on it. "Rejected" alone leaves them
guessing and they resubmit the same hours.

**Reopening is the way back from approved.** It is a separate, audited act with
its own reason. A system with no way back does not prevent corrections; it moves
them somewhere nobody is looking.

The state of the week is shown wherever time is entered — on the day view and
the week view, with the controls that are actually available. A locked week that
looked exactly like an open one until a save failed would be the worst version
of this feature: the banner is what makes the refusal predictable.

## 4b. Overtime, travel and reimbursement

Three things a contract settles that an hourly rate does not: what an hour is
worth when it is the tenth one that day, what it is worth when it is spent on a
train, and what gets paid back against what evidence
([ADR-0024](adr/0024-customer-rate-rules.md)).

**The kind of time is chosen, not derived.** An entry is work, overtime or
travel, and the person says which. A threshold on the customer produces a
**notice** on the week view — "Tuesday has 10h and none of it is marked
overtime" — in the same spirit as the unaccounted-time gaps: the tool reports
what it observed and leaves the judgement alone. Reclassifying automatically
would bill the ninth hour at a premium because somebody forgot to stop a timer,
and the person who has to defend that invoice is not the one who decided it.

**The customer's rules price each kind.** An absolute rate beats a multiplier,
because a contract naming a figure is naming it instead of a multiple. An unset
rule bills at the ordinary rate: inventing a multiplier nobody agreed to would
put an unsupported number on an invoice.

**Travel a customer does not pay for is its own state.** The time is recorded in
full and carries no amount. "We do not bill travel" and "travel is worth nothing
an hour" read differently on a timesheet, and only one of them is what was
agreed.

**Reimbursement** covers a default markup on the billable side, a mileage rate
per kilometre and a per diem per day — a distance or a number of days is priced
from the customer's rate, and the claim shows its working, because 42.5 × 2.50
is exactly the sort of arithmetic that reaches a customer wrong — and a receipt
threshold. The threshold cannot be enforced when a claim is created, since an
attachment needs something to attach to; it marks the claim there, and refuses
the week's submission until the receipt exists.

Every export carries the kind. A line billed at one and a half times the base
rate has to say why on the document carrying the figure.

## 5. Rates, billing and rounding

Rate resolution, most specific first:

```
entry override → person-on-project → project → customer → global default
```

The resolved rate is **stored on the entry** when it is billed, so a later rate
change does not silently rewrite invoiced history.

**Rounding** is applied per entry, at one documented point, before multiplication by
the rate; the applied rule is recorded on the entry
([ADR-0014](adr/0014-exact-money-and-duration.md)). Configurable per project
(increment, direction, minimum), inherited from the customer, then global.

**Expenses**: billable expenses carry an optional markup percentage; reimbursable
expenses total separately in reports because they are money owed to a person, not
to the company. Receipts attach directly.

## 6. Screens

| Screen | Purpose |
|---|---|
| **Today** | the default. Running timers, today's entries as a timeline over configurable hours, quick add, running totals (summed and elapsed), unaccounted-time gaps. |
| **Week** | grid of assignments × days, weekly totals per assignment and per day, and the week's approval state with the submit or withdraw control. |
| **Correct entry** | one entry, opened from any row: assignment, day, start, duration, note, billable. See §3. |
| **Entries** | filterable list across any range: customer, project, assignment, kind, tag, person, billable, status, free text, and time recorded on a day a project also had an expense. Every row carries its date and weekday. Paged, fifty at a time, with the total beside it. The basis of every export. |
| **Inbox** | proxy proposals awaiting your decision. |
| **Approvals** | weeks awaiting your decision, weeks you have approved (with the way to reopen one), and your own submitted weeks. Server mode only. |
| **Approval status** | a grid of people against weeks. Answers the question the queue cannot — who has *not* submitted — because an absent submission is an absent row. Weeks nobody worked stay blank so the outstanding cells are not buried. |
| **Contract terms** | one customer's overtime, travel and reimbursement rules. A screen of its own: they are read off a signed agreement and typed once, and they need labels and units to be entered correctly. |
| **Guide** | how to perform the normal actions, in numbered steps. Distinct from the `?` help, which explains the screen in front of you ([ADR-0025](adr/0025-task-oriented-guide.md)). |
| **Routines** | templates for recurring time. Offered on the day view; nothing fires them ([ADR-0027](adr/0027-routines-are-offered-not-fired.md)). |
| **Tags** | rename, recolour and tidy. Tags are created by using them, so this is not where they are defined. |
| **Calendar import** | preview and import from an .ics export, with everything that is not work shown as such by name ([ADR-0028](adr/0028-calendar-import-is-a-conversation.md)). |
| **Expenses** | list and entry, with receipts, billable/reimbursable flags. |
| **Reports** | grouped totals by customer/project/assignment/person/tag over a range, billable vs non-billable, export in five formats. |
| **Budgets and burn** | what each project has used of its cap in hours and money, worst first, with a burn rate and a projected date where the evidence supports one ([ADR-0035](adr/0035-burn-is-a-projection.md)). An overrun is shown as a number past 100% and a negative remainder rather than clamped. Where there is no honest projection the cell says which of the four reasons applies, because a blank in a "runs out" column reads as *never*. |
| **Admin** | customers, projects, assignments, rates, tags; in server mode also users, roles, memberships, sessions and the audit log. |

The day view opens with a **timeline**: the day as blocks against a clock, with
overlapping work side by side in lanes rather than one block hiding another. A
block can be dragged to move it and its lower edge dragged to resize it; both
post to the same endpoint as the plain time-and-length form each block carries,
so the whole view works with no JavaScript. Positions are CSS grid classes
rather than inline styles, because the content security policy forbids those and
weakening it for geometry would weaken it for everything.

**Which hours the pane shows** is configurable, and so is what happens to time
recorded outside them. Both answers are defensible and neither is right for
everyone, which is exactly why it is a setting: *expand* grows the pane until
everything fits, which suits somebody who works late once a month; *arrows*
keeps the window fixed and reports what fell outside it above and below the
pane, so that an hour is always the same height for somebody whose evenings are
routinely busy. The report is a count and a total — "1 later, 2h 00m from 7pm" —
because a bare arrow is not enough to decide whether to go and look. With a fixed
window a day can be full and have nothing inside it, and the pane says so rather
than claiming nothing was recorded.

**Colour is the whole line, not a dot.** A row is scanned rather than read - the
eye is looking for "the Acme rows" among forty - and a dot at the start of each
is too small a target to sort by. Each row carries a solid bar down its leading
edge and a faint tint of the same colour, verified in Go for every colour in
every theme so the text on top of it always clears WCAG AA.

Two totals are always shown together where overlap is possible: **summed** (the sum
of durations — what gets billed) and **elapsed** (the union of intervals — how much
of the day was covered). A 10-hour sum across an 8-hour footprint is then obvious
rather than alarming.

## 7. Interaction and accessibility

* **Keyboard**: `n` new entry, `s` start/stop the last assignment, `/` search,
  `g`+`t`/`w`/`r` navigation, `Esc` closes. Every action reachable without a mouse.
* **No layout shift** on fragment swaps: the running-timer header and totals update
  in place.
* **Works without JavaScript**, degraded: forms post, pages render, timers can be
  started and stopped; only the ticking clock, paste-capture and shortcuts are lost.
* Semantic HTML, labelled controls, visible focus, `aria-live` on the totals that
  change under the user, respect for `prefers-reduced-motion`.
* Colour is never the only carrier of meaning — status has a label or icon as well
  as a hue, which also matters for the colour-blind reader.

## 7a. Identity

The logotype is a clock mark and the name in two colours: **Time** in red,
**Tracker** in blue. The two halves are separate catalogue entries rather than a
substring taken at a fixed offset — a name is not obliged to split at the fourth
character in every language, and slicing a translated string by index is how one
ends up with half a grapheme. There is no whitespace between them in the markup,
so it is one word to a reader and one word to a screen reader.

Neither colour has a value of its own. `--brand-time` and `--brand-tracker` are
aliases for the entity palette's red and blue, so each theme's own pair carries
the brand: the terminal theme gets its phosphor colours, the high-contrast theme
its heavy ones, and there is no second set of sixteen values to keep in step.
Both are held to WCAG AA against the header's surface **in every theme** by the
same test that covers body text; the narrowest is autumn at 4.67:1.

**Icons** are generated, not exported. `internal/web/icongen` draws the mark at
every size from the geometry in `favicon.svg`'s viewBox, so changing the mark
means changing one shape and running `go generate ./internal/web/...` rather than
re-exporting eight files by hand — the same reasoning that puts PDF generation in
Go ([ADR-0007](adr/0007-pure-go-document-generation.md)): the build must not
depend on tooling nobody has. Coverage is computed by supersampling a distance
function, which for a ring and two round-capped segments is less code than a path
rasteriser and needs no dependency.

The set covers the three things that ask for different files:

* **tabs** — an SVG first, with 16, 32 and 48 pixel PNGs and a multi-size `.ico`
  behind it for what cannot read one.
* **iOS home screens** — a 180-pixel `apple-touch-icon`, opaque, because iOS
  composites onto black and a transparent icon arrives looking like a hole.
* **Android and desktop installs** — 192 and 512 in the manifest, plus *maskable*
  variants whose mark sits inside the middle 80%, since an adaptive icon is
  cropped to whatever silhouette the platform prefers.

Each of those rules is a test rather than a note, because every one of them is
invisible until somebody installs the application.

## 8. Themes

Eight themes, implemented purely as redefinitions of one semantic token set
([ADR-0011](adr/0011-theming-via-css-custom-properties.md)).

| Theme | Character |
|---|---|
| light | neutral greys, blue accent; the default in a light OS |
| dark | low-luminance surfaces, desaturated accents to avoid halation |
| gold | warm dark base, amber/brass accents |
| sand | warm off-white paper tones, muted terracotta accent |
| spring | fresh greens, light and high-key |
| autumn | ochre, rust and deep brown |
| terminal | green phosphor on near-black, monospace, square corners |
| high contrast | near-black on near-white, heavy borders, WCAG AA verified by test |

**terminal** is the one theme that changes the typeface as well as the colours,
and deliberately: green-on-black in a proportional face is a dark theme with an
unusual accent, not a terminal. It squares the corners, drops the shadow, adds a
faint phosphor glow and static scan lines, and puts a blinking block cursor on
the running-timer clock — the one genuinely live thing on the screen, which is
where a terminal would have put it. The glow and scan lines are removed under
`prefers-contrast: more` and `prefers-reduced-transparency`, and the cursor stops
blinking under `prefers-reduced-motion`: all three cost legibility, and none is
what somebody who set those preferences is asking for.

The choice persists per user, defaults to `prefers-color-scheme`, and is applied
before first paint so there is no flash of the wrong theme.

**Where the navigation sits** is an instance setting rather than a personal one:
it changes the shape of every screenshot in the guide and of every instruction
somebody gives a colleague over their shoulder. Like the theme it is one
attribute on `<html>` and no markup change — the same elements in the same source
order, laid out differently — so the reading order a screen reader follows and
the tab order a keyboard follows are identical either way. Below 900px the rail
becomes the top bar again, because there is no room for it.

**Clock and date formats** are instance settings too, defaulting to whatever the
interface language uses. Both supported languages write dates as `2026-08-19`,
which is the one order that cannot be misread: `03/04` means different days on
either side of the Atlantic. An administrator can still override it, because
"unambiguous" is not the same as "what the accounting department will accept",
and being told the tool is right is no help to somebody retyping every date by
hand.

**Entity colours** (assignments, projects) are chosen from a palette by *key*, and
each theme maps keys to values that work on its own background — "Acme is blue"
stays legible in all eight. The terminal theme keeps the full palette rather than
going monochrome: a phosphor tube could not have shown seven colours, but a
timesheet that cannot tell one customer from another is a worse anachronism than
a colour CRT. Icons are a curated inline-SVG set drawn in `currentColor`, plus
optional emoji.

**Every theme is held to WCAG AA on text by test**, not only the one named for
contrast — see TEST.md §3. Two themes were below it until that test was written.

## 9. Semi-automatic assistance

What is in scope, and honestly what is not: a web page cannot see which application
you are using. What it *can* do:

* **Gap detection** — the day view marks unaccounted stretches between entries
  ("2h 15m unaccounted, 13:00–15:15") and offers to fill them from a recent
  assignment.
* **Idle observation** — a running timer whose page stops running, or sits
  untouched, is reported for review as described above. Deliberately not called
  detection: what is detected is the state of a browser tab, and the difference
  between that and the state of a person is the whole design
  ([ADR-0033](adr/0033-idle-time-is-observed.md)). While the timer is still going
  it is only a notice, since its interval is still being measured; it becomes a
  question with keep, discard and split once the timer stops.
* **Overtime notices** — a day or week past the customer's agreed threshold with
  none of the time marked as overtime. Reported, never applied: see §4b.
* **Reminders** — end-of-day and end-of-week nudges: a timer still running, a day
  with nothing on it, time a colleague recorded for you, a week nobody submitted
  ([ADR-0034](adr/0034-reminders-are-shown-not-sent.md)). Shown, never sent: each
  is computed from the timesheet when a screen is drawn, so it cannot appear
  about something already dealt with, and there is no scheduler, mail or queue
  behind it. They wait for a configurable hour of *your* local afternoon — the
  week one for the last working day of the week — because a panel that is always
  there is one nobody reads. Each can be waved away for its day or week, and the
  next one asks again.
* **Suggestions from your own history** — the assignments you usually work on this
  weekday, ranked, so the common case is one click.
* **Long-running-timer review** — a timer past the maximum is flagged, not counted.

Explicitly deferred, and requiring a separate desktop agent or an integration ADR:
application/window activity tracking, calendar (ICS) import, git commit import. The
suggestion pipeline is designed so those become additional *sources* of proposals
rather than a new mechanism.

## 10. Exports

Every report renders into five formats from one description of a line, so they
cannot disagree ([ADR-0007](adr/0007-pure-go-document-generation.md)). Some
formats are handed that description as a slice and some as a stream, but a line
is built in one place either way, and the two paths are compared against each
other by test:

* **PDF** — client-presentable: title block, period, grouped entries with repeating
  table headers, totals, page numbers.
* **DOCX** — the same content as an editable document for firms that put timesheets
  into their own letterhead.
* **Markdown** — for the case none of the others cover: pasting a week into a
  ticket, a wiki, a pull request or an email. Its tables are padded so the output
  is legible as source too, because half the places it gets pasted will never
  render it.
* **CSV** — one row per entry, UTF-8 with BOM for Excel, raw and billable durations,
  rate, amount, currency.
* **JSON** — documented, versioned schema for feeding an invoicing system.

The list is one slice in the export package. The Entries screen ranges over it
and the router validates against it, so a format cannot be offered that cannot be
produced — which is what went wrong before: PDF and DOCX were written and tested
in layer 5 and the screen went on saying they arrived later.

Every download carries the whole filter the screen was showing, including the
search query — and the *whole* of it, not the page. The screen is paged at fifty
rows; an export covers everything the filter matches.

Both halves of that are load-bearing, and both were wrong. An export that quietly
covers *more* than the filter is a wrong invoice; one that covers *less* is a
worse one. Until pagination arrived the screen's row cap travelled into the
export with the rest of the filter, so any range with more entries than the cap
was truncated in place — oldest first, silently, in a file somebody was about to
bill from, and to a *different* extent depending on which page they happened to
be looking at.

A whole-filter export can be large, so **CSV and JSON stream**. The rows arrive
from a database cursor and are written and released one at a time; nothing holds
the report. The JSON schema is what makes that possible — its totals come after
its entries, so they can be folded from the rows as they go past rather than
computed from a document that has to exist first. Had the totals been declared
first, this format could not stream at all.

That folding is where the design has to be careful. A summed total is addition
and does not care about order, but *elapsed* time is the union of overlapping
intervals, and a union can only be accumulated in one pass if the intervals
arrive in ascending order — out of order, a later interval can cover a run that
has already been closed and counted. So the streaming query orders ascending
(the screen's listing orders descending, which is the opposite), and the
accumulator records whether it ever saw an interval out of order. If it did, the
export fails rather than emitting a total that is quietly too large: a wrong
number on an invoice is not a thing to find out about later.

Streaming costs something honest: a response commits to its status code with
its first byte, so a failure halfway through ten thousand rows is a truncated
download and a log line, not an error page. What it does not have to cost is the
*first* row. Most export failures — a malformed regular expression above all —
are knowable before any byte is due, so one row is pulled before a single header
is set, and those are answered with the message that says how to fix them
instead of with a file containing nothing but a header row and a 200, which
reads as "no results" rather than as "your search was wrong".

**PDF, DOCX and Markdown do not stream**, and are not made to. Each genuinely
needs the whole report — to break pages and repeat headers, to size a table, to
put a total in a heading. Rather than let them fail at whatever size a particular
machine runs out at, they count the matching rows first and refuse above 50,000
with 413 and a message naming CSV and JSON, which have no such limit. A refusal
that says what to do instead is worth more than a truncation that says nothing,
which is what this screen used to do.

Exports respect authorisation: a `client` user's export contains only their
customer's confirmed entries, with internal notes and cost data removed before the
data leaves the service layer.

That removal is a transformation of the value rather than a condition in a
template ([ADR-0008](adr/0008-rbac-model.md)). A client's screen is one screen -
the work done for them, and the downloads of it - and what reaches it has already
had the note, the rate, the amount, the currency, the rounding rule, the proxy
authorship, the tags and the attachment count taken out of it. The catalogue is
narrowed the same way, because a customer row carries a negotiated rate and a
project row carries a budget. Nothing that is not confirmed reaches them at all,
and that is applied in the query rather than afterwards, so the count behind the
pager is the number of rows they can be shown.

## 10a. Backups

A backup is a **zip archive**: the JSON document, a readme, and every attachment
in its original bytes, named by content hash
([ADR-0030](adr/0030-encrypted-backup-archives.md)). A backup with a timesheet
and no receipts is not a backup — the evidence behind a billed expense has to
travel with it.

Setting a backup password in Settings encrypts every entry with **AES-256 in the
WinZip AE-2 scheme**, which 7-Zip, WinZip, Keka and most desktop archive managers
already read. That interoperability is the point: an archive only this binary can
open is not a backup. The password defends an archive that has left the machine,
not the database it came from — anybody who can read the stored password already
has every row it was made from.

Restore accepts an encrypted zip, a plain zip, or a bare JSON document from
before archives existed, sniffed from the first bytes rather than the file name.
It still merges: a record already present is skipped, so restoring twice is
harmless.

## 11. Deliberate non-goals

Invoicing and payments (we export for the system that does that), payroll,
project management and task tracking, chat, and mobile native apps — the UI is
responsive, but a native client is a different product.
