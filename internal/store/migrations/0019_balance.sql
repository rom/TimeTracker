-- 0019_balance.sql - the running over or under.
--
-- Expected hours arrived in 0017 and answered one week at a time: this week is
-- 37h 30m of 40h, so two and a half hours short. The question that follows it
-- immediately is the one flexitime actually turns on - am I up or down overall
-- - and nothing could answer it, because nothing knew where to start counting.
--
-- A date, then, rather than a stored running figure. Deriving it from the
-- entries means it cannot drift out of step with them: correcting last March
-- corrects the balance, and a balance column would have needed every write to
-- remember to adjust it.
--
-- Empty means no balance is shown at all, and that is the default. A running
-- figure is read as hours owed, in both directions, by people who did not
-- choose to be measured that way; an instance that wants it says so once, by
-- naming the day the count starts. A person may start later than the instance,
-- which is how somebody who joined in June is not born owing five months.
ALTER TABLE settings ADD COLUMN balance_starts_on TEXT NOT NULL DEFAULT '';
ALTER TABLE users    ADD COLUMN balance_starts_on TEXT NOT NULL DEFAULT '';
