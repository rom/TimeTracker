# ADR-0013: Content-addressed attachments on disk

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-013, ASR-010, ASR-005

## Context

Users attach files and photos to time entries and expense receipts, and paste images
straight from the clipboard. Receipts in particular are photographs: a few megabytes
each, and the same receipt is often attached twice.

Storing blobs in SQLite is possible and keeps everything in one file — attractive
for backup. But large blobs inflate the database, make the WAL churn, and mean every
`VACUUM` and every naive backup copies gigabytes of JPEG. Storing them on disk
splits the durability story instead.

## Decision

Attachment **bytes live on disk**, **metadata lives in the database**.

* Files are **content-addressed**: the storage path is derived from the SHA-256 of
  the content (`blobs/ab/cd/abcd…`), so an identical file attached twice is stored
  once and referenced twice. Deleting an attachment removes the reference; the blob
  is removed when its last reference goes, by an explicit sweep rather than inline.
* The database row holds original filename, MIME type, size, hash, uploader, owning
  record and timestamp. The original filename is **never** used as a path component,
  which removes path traversal and case-collision problems on Windows and macOS in
  one move.
* **Serving is always through an authorising handler.** The blob directory is never
  exposed as a static file route: an attachment is readable only by a user permitted
  to read the record that owns it. Responses set `Content-Disposition: attachment`,
  a strict `Content-Type` from a server-side sniff (never the client's claim), and
  `X-Content-Type-Options: nosniff`.
* **Uploads are validated**: a configurable size cap, a total-per-record cap, an
  allow-list of types (images, PDF, common office and text formats), and a
  server-side content sniff that must agree with the extension. SVG was refused
  outright here; it is accepted since [ADR-0031](0031-attachment-previews.md),
  which replaced the refusal with a preview route that serves it inside an
  `<img>` behind a response policy forbidding scripts and every subresource. It
  is still never served as a document.
* **Pasted images** arrive as a clipboard blob, are given a generated name, and
  otherwise follow exactly the same path — there is no second, laxer upload route.
* **Backups include the blob directory** (ADR-0009), because a backup with a
  timesheet but no receipts is not a backup. This was true on paper and false in
  the code until [ADR-0030](0030-encrypted-backup-archives.md) made a backup a
  zip carrying the attachments themselves.

## Consequences

**Positive**

* The database stays small and fast, which protects ASR-012.
* Deduplication is free and meaningful for repeatedly attached receipts.
* Content addressing gives integrity verification for nothing: re-hash and compare.
* One authorisation path for every byte served.

**Negative / accepted costs**

* Two things to keep consistent. A crash between writing the blob and committing the
  row leaves an orphan file; we write the blob first, then commit, so the failure
  mode is a harmless orphan (swept later) rather than a database row pointing at
  nothing.
* Backup and restore must handle both stores, and a user copying only the `.db` file
  gets a surprise. Documented, and the built-in backup produces one archive.
* Deduplication means a blob is shared across records, so deletion needs
  reference counting — done in the sweep, not on the request path.
* Local mode and server mode must agree on where the data directory lives per
  platform.

## Alternatives considered

**Blobs in SQLite** — one file, atomic with the metadata, trivially backed up.
Genuinely close; rejected on database size and backup cost once receipts are
photographs, and because serving a large blob from SQLite means holding it in memory
or streaming through the driver.

**Store under the original filename** — friendlier on disk. Rejected: path
traversal, collisions, case-insensitivity differences across the three target
platforms, and no deduplication.

**External object storage (S3-compatible)** — the right answer at scale. Rejected on
ASR-003: it cannot be a requirement for a laptop user. The blob store is behind an
interface, so this is a future ADR rather than a rewrite.

## Related

* ADR-0009 (backups), ADR-0008 (authorisation)
