# ADR-0009: Embedded assets and forward-only migrations

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-003, ASR-010

## Context

ASR-003 requires that copying one file onto a machine yields a working application.
That means templates, CSS, JavaScript, fonts and SQL must travel inside the binary,
not beside it. ASR-010 requires that upgrading the binary never loses data, which
means the schema has to evolve under a database the user already has, with no
migration tool installed.

Development pulls the other way: editing a template and having to rebuild to see the
change is a poor loop.

## Decision

**Assets** (`html/template` files, CSS, JS, fonts, migration SQL) are embedded with
`//go:embed`. A build tag `dev` swaps the embedded filesystem for a
directory-backed one that reads from disk and re-parses templates per request, so
development keeps a fast loop while releases stay self-contained.

**Migrations** are numbered SQL files (`0001_init.sql`, `0002_….sql`), applied at
startup:

* **forward-only.** No down migrations. A down migration is either trivial (and so
  the forward fix is trivial too) or it destroys data, and it is invariably the
  least-tested code in the repository. Recovery is by restoring a backup, which is
  a path we test.
* each runs in a **transaction**, recorded in `schema_migrations` with its version,
  a checksum of the file, and when it was applied. A changed checksum for an
  already-applied migration is a hard startup failure: it means someone edited
  history, and continuing would silently diverge schemas between installations.
* the binary **refuses to start** against a database whose recorded version is
  *newer* than the migrations it carries — that is a downgraded binary pointed at
  upgraded data, and running would corrupt it.
* an **automatic backup copy** of the database file is taken before the first
  migration of a run that has anything to apply, so a failed upgrade is recoverable
  even without a user backup.

**Backup/restore** produces a single archive containing a consistent database
snapshot (SQLite online backup, not a raw file copy of a live WAL database) plus the
attachment blobs, and restores into a clean installation.

## Consequences

**Positive**

* One file to deploy; no "forgot to copy the templates" failure mode.
* Upgrades are automatic and safe by default, which suits a user who is not an
  operator.
* Checksums catch the "someone edited an applied migration" mistake at the only
  moment it is still cheap.

**Negative / accepted costs**

* No rollback path other than restore-from-backup. Accepted deliberately, and it
  raises the bar on reviewing migrations before release.
* Assets are compiled in, so a user cannot tweak the CSS without rebuilding. An
  explicit override directory for themes is a candidate later feature.
* The pre-migration backup costs disk space and time proportional to the database
  size on upgrade runs.

## Alternatives considered

**Assets on disk next to the binary** — trivially editable. Rejected on ASR-003 and
because it invites version skew between binary and templates.

**Up/down migrations** — conventional, and reversible in principle. Rejected as
argued above: down migrations that drop columns lose data, and are usually never
executed until the one bad night when they are.

**An external migration tool (goose, migrate CLI)** — mature. Rejected on ASR-003:
another binary the user must install before their data works.

**ORM auto-migration** — rejected: it infers destructive changes, and "infer" is not
a property we want anywhere near the user's billing history.

## Related

* ADR-0002 (server-rendered), ADR-0003 (SQLite), ADR-0013 (attachments in backups)
