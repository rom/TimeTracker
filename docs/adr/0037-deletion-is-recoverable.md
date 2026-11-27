# ADR-0037: A deletion is recoverable, not merely recorded

* **Status:** accepted
* **Date:** 2026-11-27
* **Addresses:** ASR-010, ASR-020

## Context

The catalogue has never destroyed anything. A customer, a project and an
assignment are archived rather than deleted, and the administration screen has
offered Restore beside Archive since the beginning, because an invoice issued
last year must still be able to say who was billed.

Everything else was destroyed outright: a time entry, an expense, a tag, a
routine, a set of contract terms. The audit trail recorded that the deletion
happened, who did it and a short description of what went — enough to answer
"who deleted my Tuesday", and not enough to put Tuesday back.

Two things made that worse than it sounds:

* **The confirmation was JavaScript.** `hx-confirm` is a `window.confirm` in
  `app.js`. This application works without script by design, and without script
  the confirmation simply was not there: one click, and an entry was gone
  permanently. The one control standing between a misplaced click and
  irreversible data loss was the one part of the interface that silently
  disappeared.
* **A tag deletion is not local.** Deleting a tag takes it off every entry
  carrying it, across every project. The audit row recorded the name and a
  count; the association rows were gone.

## Decision

**Deleting a record moves it to a recycle bin, in the same transaction that
removes it. Restoring puts it back under its original identity.**

### The capture is generic

At deletion the row is read with `SELECT *` and stored as a map of column name
to value; at restore it is written back by those names. There is no per-table
serialiser anywhere.

This is the decision that matters. Six hand-written serialisers would be six
things to keep in step with six schemas, and the failure mode is silent: a
column added by a later migration is simply dropped on restore, and nobody
finds out until a restored entry is missing a field nobody thought to check.

Values travel as strings or nulls. Every column in the covered tables has TEXT
or INTEGER affinity and none holds a blob, so a string round trip is lossless —
SQLite applies the column's affinity on the way back in. Numbers through JSON
would not be: `encoding/json` decodes every number into a `float64`, which
turns `3600` into `3600.0` and rounds an id past 2⁵³ outright.

### Children travel with their parent

A time entry without its tags is not the entry that was deleted, so the capture
names a list of tables and the write-back order is a parent before the rows
that reference it. That order is also the allow-list: a payload naming a table
this version does not know — written by a newer one — is refused rather than
half-applied, because a half-restored record looks like a recovery.

### The original identity is kept

SQLite's `AUTOINCREMENT` never reuses an id, so a row goes back where it was and
anything that referred to it by id still does.

### The lock still applies

Restoring is a way of putting hours into a week, so it is checked against the
timesheet lock exactly as a new entry is (ADR-0023). An entry deleted while the
week was open and restored after it was approved would otherwise change figures
somebody had signed off, without the visible, audited reopen.

### The bin does not replace the audit trail

Both are written, in the same transaction, as before. The bin answers "can I
have it back"; the trail answers "who did this". The second question outlives
the first.

### The server asks the second time

`app.js` now sends `confirmed=1` once the person has answered its dialog, and a
destructive request arriving without it gets a confirmation page naming the
record. With script, the page is never seen. Without it, that page is the whole
confirmation, which before this was nothing at all.

It only asks about records the service agrees exist: a confirmation rendered
for a record that is about to be refused would turn a refusal into a page, and
answer a question the person has no business asking.

## Consequences

**Positive**

* A deletion is a decision that can be revisited, which is what makes the
  confirmation a courtesy rather than the last line of defence.
* A tag deletion — the widest-reaching destructive act in the application — is
  recoverable along with every association it removed.
* `DeleteRoutine` gained an audit row it never had: it was the one deletion
  that went straight to the store, so a routine could disappear leaving nothing
  behind that said so.
* The capture survives migrations without being touched.

**Negative / accepted costs**

* Deleted rows are kept indefinitely. On a timesheet-sized database this is
  measured in kilobytes, and a retention policy would be a second decision — on
  a feature whose entire point is not losing things — so it is deliberately not
  made here.
* Restoring an expense whose receipt file has since been swept gives back a row
  pointing at a missing blob. The attachment reader already reports that state,
  because a backup restored without its blobs produces it, and it is a better
  answer than an expense that comes back silently claiming it never had one.
* Attachments deleted on their own are not covered: the blob is gone by the
  time the row is, and a bin entry that cannot produce the file would be a
  promise the application cannot keep.
* A programmatic caller that posts a deletion must now send `confirmed=1`.

## Alternatives considered

**Restore from the audit trail.** The trail already records deletions, so no
new table would be needed. Rejected: the details are partial by design and
widening them to whole rows changes what the trail shows people — it is read by
more people than the records are — and an append-only history is the wrong
shape for a work queue.

**Soft delete with a `deleted_at` column.** The usual answer, and it would make
restore trivial. Rejected because the filter has to be added to every query
that touches the table, and the failure mode is a deleted entry quietly
appearing in a total. The bin touches the delete path and nothing else.

**A retention window.** Deferred, not rejected. See the costs above.

## Related

* [ADR-0023](0023-week-as-the-unit-of-approval.md) — the lock a restore is
  checked against.
* [ADR-0008](0008-rbac-model.md) — who may restore what: a record with an owner
  is that person's, one without belongs to the instance.
