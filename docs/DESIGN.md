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
* **idle detection**: after a configurable idle period the app offers *keep*,
  *discard the idle time*, or *split into a separate entry*;
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
| **Today** | the default. Running timers, today's entries as a timeline, quick add, running totals (summed and elapsed), unaccounted-time gaps. |
| **Week** | grid of assignments × days, weekly totals per assignment and per day, and the week's approval state with the submit or withdraw control. |
| **Correct entry** | one entry, opened from any row: assignment, day, start, duration, note, billable. See §3. |
| **Entries** | filterable list across any range: customer, project, assignment, tag, person, billable, status. The basis of every export. |
| **Inbox** | proxy proposals awaiting your decision. |
| **Approvals** | weeks awaiting your decision, weeks you have approved (with the way to reopen one), and your own submitted weeks. Server mode only. |
| **Expenses** | list and entry, with receipts, billable/reimbursable flags. |
| **Reports** | grouped totals by customer/project/assignment/person/tag over a range, billable vs non-billable, budget consumption, export in four formats. |
| **Admin** | customers, projects, assignments, rates, tags; in server mode also users, roles, memberships, sessions and the audit log. |

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

## 8. Themes

Seven themes — light, dark, gold, sand, spring, autumn, high contrast — implemented
purely as redefinitions of one semantic token set
([ADR-0011](adr/0011-theming-via-css-custom-properties.md)).

| Theme | Character |
|---|---|
| light | neutral greys, blue accent; the default in a light OS |
| dark | low-luminance surfaces, desaturated accents to avoid halation |
| gold | warm dark base, amber/brass accents |
| sand | warm off-white paper tones, muted terracotta accent |
| spring | fresh greens, light and high-key |
| autumn | ochre, rust and deep brown |
| high contrast | near-black on near-white, heavy borders, WCAG AA verified by test |

The choice persists per user, defaults to `prefers-color-scheme`, and is applied
before first paint so there is no flash of the wrong theme.

**Entity colours** (assignments, projects) are chosen from a palette by *key*, and
each theme maps keys to values that work on its own background — "Acme is blue"
stays legible in all seven. Icons are a curated inline-SVG set drawn in
`currentColor`, plus optional emoji.

## 9. Semi-automatic assistance

What is in scope, and honestly what is not: a web page cannot see which application
you are using. What it *can* do:

* **Gap detection** — the day view marks unaccounted stretches between entries
  ("2h 15m unaccounted, 13:00–15:15") and offers to fill them from a recent
  assignment.
* **Idle detection** — on running timers, as described above.
* **Reminders** — end-of-day and end-of-week nudges for missing time and pending
  proposals.
* **Suggestions from your own history** — the assignments you usually work on this
  weekday, ranked, so the common case is one click.
* **Long-running-timer review** — a timer past the maximum is flagged, not counted.

Explicitly deferred, and requiring a separate desktop agent or an integration ADR:
application/window activity tracking, calendar (ICS) import, git commit import. The
suggestion pipeline is designed so those become additional *sources* of proposals
rather than a new mechanism.

## 10. Exports

Every report renders from one in-memory value into four formats, so they cannot
disagree ([ADR-0007](adr/0007-pure-go-document-generation.md)):

* **PDF** — client-presentable: title block, period, grouped entries with repeating
  table headers, totals, page numbers.
* **CSV** — one row per entry, UTF-8 with BOM for Excel, raw and billable durations,
  rate, amount, currency.
* **DOCX** — the same content as an editable document for firms that put timesheets
  into their own letterhead.
* **JSON** — documented, versioned schema for feeding an invoicing system.

Exports respect authorisation: a `client` user's export contains only their
customer's confirmed entries, with internal notes and cost data removed before the
data leaves the service layer.

## 11. Deliberate non-goals

Invoicing and payments (we export for the system that does that), payroll,
project management and task tracking, chat, and mobile native apps — the UI is
responsive, but a native client is a different product.
