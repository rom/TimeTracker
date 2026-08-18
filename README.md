# TimeTracker

A time tracking application for billable work: customers, projects and
assignments, with **several timers running at once**, because a working day is
not a sequence of disjoint blocks.

One Go binary. No runtime dependencies, no database server, no Node.js. Runs on
macOS, Linux and Windows.

## Quick start

```sh
make build          # produces bin/timetracker
./bin/timetracker   # local mode: http://127.0.0.1:8420
```

It creates its own database on first run and needs no configuration. Open the
address it prints, add a customer, a project and an assignment under **Admin**,
and start tracking.

```sh
./bin/timetracker --help          # all flags
./bin/timetracker --open          # launch a browser too
make build-all                    # cross-compile all six OS/arch targets
make check                        # format, vet, lint and the full test suite
```

## What it does today

* **Concurrent timers.** Any number at once. Overlaps are recorded and shown,
  never silently split between assignments.
* **Two totals, always together.** *Tracked* (what gets billed) and *elapsed*
  (wall-clock coverage), so a ten-hour sum across an eight-hour day explains
  itself instead of looking like an error.
* **Quick add.** `2h acme/migration fixed the login redirect #travel`. Anything
  ambiguous opens the form pre-filled rather than being guessed at.
* **Day, week and entry views**, with unaccounted-time gaps surfaced as prompts.
* **Assignments with colours and icons**, so the day view is scannable.
* **Exact money.** Integer minor units throughout, layered rate resolution
  (assignment → project → customer → default), and the rate and rounding rule
  stored on each entry — so changing a rate tomorrow cannot alter what was
  invoiced today.
* **Seven themes:** light, dark, gold, sand, spring, autumn, high contrast.
* **CSV and JSON export** of any filtered range.
* **Audit trail.** Every mutation records who did what, when, in the same
  transaction as the change itself.

## Not yet

Server mode (authentication, RBAC, rsyslog) refuses to start rather than
pretending; PDF and DOCX export return 501; attachments, expenses, colleague
proxy entries and approvals are designed but not built. See
[docs/MVP_PLAN.md](docs/MVP_PLAN.md) for the sequence.

## Documentation

| Document | Contents |
|---|---|
| [MVP_PLAN.md](docs/MVP_PLAN.md) | what ships when, and the risks |
| [DESIGN.md](docs/DESIGN.md) | domain concepts, screens, interaction, themes |
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | structure, data model, request lifecycle |
| [ASR.md](docs/ASR.md) | the 14 requirements that constrain the architecture |
| [adr/](docs/adr/README.md) | 15 decision records — why things are the way they are |
| [TEST.md](docs/TEST.md) | test strategy, and what proves each requirement |
| [SECURITY.md](docs/SECURITY.md) | threat model, controls, and their limits |
| [CHANGELOG.md](docs/CHANGELOG.md) | what changed |

## Licence

See [LICENSE](LICENSE).
