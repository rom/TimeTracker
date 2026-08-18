# ADR-0021: Backups are JSON, and restoring merges

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-010, ASR-018

## Context

Someone reaching for a backup is already having a bad day. Whatever the restore
does, it must not make that day worse.

Two questions decide the design. What is in the file, and what happens when it
goes back in.

The obvious file format is a copy of the SQLite database. It is exact, trivial
to produce, and restores by overwriting. It is also opaque, cannot be partial,
and cannot be restored into an instance that has moved on since - which is the
common case, because people restore after losing *some* work, not all of it.

The obvious restore semantic is "replace everything", which is what a database
copy forces. That turns a mistaken restore into data loss: restore last week's
backup to recover one deleted customer, and this week's work is gone.

## Decision

**A backup is a single JSON file, and restoring merges.**

The file carries customers, projects, assignments, time entries and expenses,
with catalogue records referenced **by name rather than by id**. Ids from another
instance are meaningless; restoring by id would either collide with existing
records or attach work to the wrong customer.

**Partial by construction.** The same format covers everything, one customer, one
project, or one date range. A partial backup is a normal backup that contains
less, and it says so in a `scope` field - so it cannot be mistaken for a complete
one that lost most of its data.

**Restore merges, never replaces.** A record already present is skipped. Identity
for a time entry is the subject, the assignment and the start instant: two
entries at the same second on the same assignment for the same person are the
same entry. The consequences are the ones that matter after a mistake:

* restoring the same file twice is harmless;
* restoring an old backup does not delete newer work;
* a restore can be tried, inspected, and tried again.

**A newer format version is refused**, rather than read on a best-effort basis. A
file from a later version may carry fields this one would silently drop, and a
restore that quietly loses data is the worst possible outcome for a backup.

**Automatic backups are off by default.** Writing copies of someone's data on a
schedule they did not ask for is their decision, not ours. When enabled they run
on an interval, keep the newest N, and a retention of zero removes nothing
rather than being read as "delete everything".

## Consequences

**Positive**

* A backup can be inspected, diffed, and partially restored. Someone who deleted
  one customer can recover exactly that customer.
* No restore can destroy data, which makes the feature safe to reach for under
  pressure - the only time it is ever used.
* The format is readable by anything, so the data is not hostage to this
  application.

**Negative / accepted costs**

* A JSON backup is larger and slower to produce than a file copy, and a very
  large instance would feel it.
* **Merge cannot express a deletion.** If a record was deleted deliberately after
  the backup was taken, restoring brings it back. That is the deliberate trade:
  resurrection is recoverable, deletion is not.
* Matching by name means renaming a customer and then restoring an old backup
  creates a second customer under the old name. Name is the only identity that
  survives crossing instances, so this is inherent rather than incidental.
* Attachments are **not** in the file - only their metadata. The blobs live on
  disk and must be copied separately, which is stated in the help text and is a
  real gap in "one file holds everything".
* Users, roles and audit history are excluded: a backup is someone's own work,
  not the instance's configuration or its security record.

## Alternatives considered

**Copy the SQLite file** - exact and trivial. Rejected on partial backups and on
merge, neither of which it can do, and because the result is unreadable without
this application.

**Replace on restore** - simpler, and what most people first expect. Rejected: it
converts a mistaken restore into data loss, at exactly the moment the user is
least able to afford one.

**Include attachment bytes as base64** - genuinely one file. Rejected for now:
it inflates the JSON several-fold and makes a text format unreadable. A future
ADR could add a zip container holding the JSON plus the blobs.

## Related

* ADR-0009 (migrations and durability), ADR-0013 (attachment storage)
