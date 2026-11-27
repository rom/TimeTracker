-- 0016_personal_presentation.sql - presentation follows the person.
--
-- Theme and language have always been the account's: they are stored on the
-- user, they follow somebody to another device, and nobody else is consulted.
-- Four other choices that are just as personal were not. The hours the day
-- pane covers, whether a clock reads 24-hour, how a date is written and where
-- the navigation sits were all instance-wide and settable only by an
-- administrator - so on a server with twenty people, one of them picked
-- everybody's working hours, and somebody who starts at six and finishes at
-- two read their day in a pane drawn for nine-to-five.
--
-- Each column is empty, or -1 for an hour, meaning "whatever the instance
-- says". That is what makes this migration free: every existing account
-- follows the instance exactly as it did before, and a value is stored only
-- once somebody deliberately chooses one.
--
-- Deliberately NOT moved: the week's first day. It decides which Monday a
-- timesheet period is keyed by, so two people disagreeing about it would be
-- two people disagreeing about which week was approved
-- (docs/adr/0023-week-as-the-unit-of-approval.md). It stays the instance's.
ALTER TABLE users ADD COLUMN clock_format   TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN date_format    TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN nav_position   TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN day_overflow   TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN day_start_hour INTEGER NOT NULL DEFAULT -1;
ALTER TABLE users ADD COLUMN day_end_hour   INTEGER NOT NULL DEFAULT -1;
