# ADR-0030: A backup is a zip archive, optionally AES-encrypted with a stored password

* **Status:** accepted
* **Date:** 2026-08-19
* **Addresses:** ASR-013, ASR-019

## Context

A backup was a single JSON document. That was the right shape for the data and
the wrong shape for everything else the data depends on.

**Attachments were not in it.** ADR-0013 says plainly that "a backup with a
timesheet but no receipts is not a backup", and the backup did not have the
receipts. A billed expense whose evidence lives only on a disk that has since
died is an expense that cannot be defended. Base64 inside the JSON was never an
option: it inflates by a third, and the bulk of a real backup is photographs.

**A backup is the most sensitive file this application produces.** It carries
every note, every rate, every client name and every receipt, in one file that is
explicitly meant to be copied elsewhere — mailed to an accountant, put on a
memory stick, synced to somebody's cloud drive. The database it came from lives
at 0600 on one machine; the backup lives wherever it was sent.

**Expenses were collected and never restored.** `RestoreResult` had a field for
them; nothing filled it. A latent bug, but one that made the attachment work
impossible: receipts hang off expenses, and there were no restored expenses to
hang them on.

## Decision

**A backup is a zip archive holding `backup.json`, a `README.txt`, and every
attachment under `attachments/` named by its SHA-256. When a backup password is
set in the settings, every entry is encrypted with AES-256 in the WinZip AE-2
scheme.**

Three things follow from that choice and each was decided on its own:

**AE-2 rather than a format of our own.** The point of a backup is that it can
be opened in five years by somebody who may not have this binary. AE-2 is what
7-Zip, WinZip, Keka and most desktop archive managers already read. A container
of our own devising would have been less code and would have made the password
useless to anybody but us. Legacy ZipCrypto is not offered at all: it is broken
by a known-plaintext attack in seconds, and offering it would be worse than
offering nothing because it looks like protection.

**The password is stored as it was typed.** This needs saying out loud. It
cannot be hashed — the application has to *use* it to encrypt the next scheduled
backup, and a hash encrypts nothing. It cannot be encrypted either, because the
key would have to sit beside it in the same file.

What makes that acceptable is naming the threat honestly. This password defends
**an archive that has left the machine**. It does not defend the database, and
it never could: anybody who can read the `settings` row already has every row
the backup was made from. The password is never rendered back into the form,
never logged, and stripped from the settings struct before it reaches a
template, so a careless `{{.Settings.BackupPassword}}` cannot put it on screen.
The audit log records *that* it changed, never what it became.

**Restore accepts all three shapes**: an encrypted zip, a plain zip, and a bare
JSON document from before archives existed — sniffed from the first four bytes
rather than from the file name. A backup format that abandons its own old files
is not a backup format.

## Consequences

**Positive**

* A backup now contains the evidence behind the figures, which is what ADR-0013
  said it should.
* An archive can be handed to somebody, or stored somewhere less trusted, with a
  password that any ordinary archiver understands.
* Expenses restore, which they did not before.
* The readme means an archive found in five years explains itself.

**Negative / accepted costs**

* Encrypted entries are buffered whole in memory: the header must carry the
  compressed size, which is not known until compression has happened. Bounded by
  the blob store's 25 MiB per-file limit, and only for encrypted archives —
  plain ones still stream.
* AE-2 fixes the key derivation at 1000 PBKDF2-HMAC-SHA1 iterations. That is low
  by modern standards and cannot be raised without producing archives no other
  tool can read, which is the whole trade. The advice to users is therefore
  about the length of the password, and the settings screen says so.
* Info-ZIP's `unzip` cannot read AES entries and reports "unsupported
  compression method 99". The readme inside every archive says which tools can.
* Entry *names* are not encrypted by this format. They are content hashes and
  fixed strings, so they leak how many attachments exist and nothing else — but
  it is a property of the format, not an oversight, and the test says so.

## Alternatives considered

**Copy the SQLite file.** Opaque, cannot be partial, and cannot be merged into
an instance that has moved on — all the reasons the JSON document was chosen
originally, unchanged.

**Base64 the attachments into the JSON.** Inflates the bulk of the archive by a
third and makes the document unreadable by the human it was written plain for.

**Encrypt with age or a NaCl secretbox.** Better cryptography, and a file only
this application can open. That is exactly backwards for a backup.

**Ask for the password at download and restore rather than storing it.** It
would have removed the stored secret entirely, but scheduled backups have nobody
to ask, and a backup that runs unattended is the one that saves somebody.

## Related

* ADR-0009 — embedded assets and migrations
* ADR-0013 — attachment storage
* ADR-0031 — attachment previews
