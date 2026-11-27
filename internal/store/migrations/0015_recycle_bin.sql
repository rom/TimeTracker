-- 0015_recycle_bin.sql - somewhere for deleted things to go.
--
-- The catalogue has never destroyed anything: a customer, a project and an
-- assignment are archived, and the admin screen has always offered Restore
-- beside Archive, because an invoice issued last year must still be able to say
-- who was billed. Everything else was destroyed outright - a time entry, an
-- expense, a tag, a routine, a contract term - with the audit log recording
-- that it happened and a partial description of what it was. Enough to answer
-- "who deleted my Tuesday", not enough to put Tuesday back.
--
-- The payload is a generic capture rather than a column list: at deletion the
-- whole row is read with SELECT *, and at restore it is written back by the
-- column names it was captured under. That is deliberate. A hand-written
-- serialiser per table would be six serialisers to keep in step with six
-- schemas, and the failure mode - a column added by a later migration and
-- silently dropped on restore - is invisible until somebody restores a record
-- and quietly loses a field.
--
-- Child rows travel with their parent, keyed by table name, because a time
-- entry without its tags is not the entry that was deleted.
--
-- The original id is preserved. SQLite's AUTOINCREMENT never reuses an id, so
-- putting the row back where it was cannot collide with a later record, and
-- anything pointing at it by id - an attachment on an expense, a tag link -
-- still points at the right thing.
CREATE TABLE deleted_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 'time_entry' | 'expense' | 'tag' | 'routine' | 'contract_term'
    kind        TEXT    NOT NULL,
    -- The id the record had, so a restore puts it back where it was.
    record_id   INTEGER NOT NULL,
    -- Whose record it was, for scoping the list to the person who may see it.
    -- Zero where the record belongs to the instance rather than a person, as a
    -- tag does.
    user_id     INTEGER NOT NULL DEFAULT 0,
    -- A sentence describing what was deleted, written at deletion time: the
    -- screen cannot rebuild it from the payload without knowing every schema,
    -- and the person choosing what to restore needs to read it.
    summary     TEXT    NOT NULL,
    -- JSON: {"table name": [ {column: value-or-null}, ... ], ...}
    payload     TEXT    NOT NULL,
    deleted_by  INTEGER NOT NULL REFERENCES users(id),
    deleted_at  TEXT    NOT NULL
);

-- The screen lists newest first, scoped to a person.
CREATE INDEX idx_deleted_records_at ON deleted_records(deleted_at DESC);
CREATE INDEX idx_deleted_records_user ON deleted_records(user_id, deleted_at DESC);
