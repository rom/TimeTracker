-- 0010_presentation_settings.sql - how the application presents itself.
--
-- Five settings, all instance-wide, all of them things that genuinely differ
-- between organisations rather than preferences the designer declined to make:
--
--   * where the navigation sits,
--   * whether the clock is 12- or 24-hour and how a date is written,
--   * which hours the day pane shows by default,
--   * what happens to time recorded outside those hours.
--
-- The last pair is the interesting one. Until now the day pane grew to cover
-- whatever was recorded, which is right for somebody who occasionally works late
-- and wrong for somebody whose evenings are routinely busy: their working day
-- gets squeezed into the top third of the pane every single day. Neither
-- behaviour is correct for everyone, so it became a choice.

ALTER TABLE settings ADD COLUMN nav_position TEXT NOT NULL DEFAULT 'top';

-- 'auto' means "follow the language": Swedish writes 2026-08-19 and 14:30,
-- English-speaking readers are split, and both are better served by an explicit
-- choice than by a guess. It is the default because it is what the application
-- did before this migration existed, and an upgrade must not silently reformat
-- every date in the interface.
ALTER TABLE settings ADD COLUMN clock_format TEXT NOT NULL DEFAULT 'auto';
ALTER TABLE settings ADD COLUMN date_format  TEXT NOT NULL DEFAULT 'auto';

-- The default window of the day pane, as whole hours. 8 to 18 is what the
-- timeline was hard-coded to before this row could say otherwise.
ALTER TABLE settings ADD COLUMN day_start_hour INTEGER NOT NULL DEFAULT 8;
ALTER TABLE settings ADD COLUMN day_end_hour   INTEGER NOT NULL DEFAULT 18;

-- 'expand' grows the pane to cover everything recorded; 'arrows' keeps the
-- window fixed and marks what falls outside it. 'expand' is the default because
-- it is the existing behaviour, and because a first-time user is better served
-- by seeing their time than by seeing an arrow telling them it is elsewhere.
ALTER TABLE settings ADD COLUMN day_overflow TEXT NOT NULL DEFAULT 'expand';

-- The password that encrypts a backup archive, or '' for an unencrypted one.
--
-- Stored as it was typed, which needs justifying. It cannot be hashed: this
-- application has to *use* it to encrypt the next scheduled backup, and a hash
-- cannot encrypt anything. It cannot be encrypted either, because the key would
-- have to live beside it in the same file.
--
-- What makes that acceptable is the threat this password actually defends
-- against: a backup archive that has left the machine - mailed to an
-- accountant, copied to a memory stick, synced to somebody's cloud drive. An
-- attacker who can read this column already has the database the backup was
-- made from, so the password protects nothing they do not already hold. It is
-- never rendered back into the form and never logged.
--
-- See docs/adr/0030-encrypted-backup-archives.md.
ALTER TABLE settings ADD COLUMN backup_password TEXT NOT NULL DEFAULT '';
