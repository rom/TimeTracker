-- 0003_language.sql - the user's interface language.
--
-- Stored per user rather than only in the browser, so the choice follows a
-- person between devices, and so the server can render the correct language in
-- the very first response instead of the page arriving in English and being
-- corrected by script afterwards.
--
-- Empty means "not chosen": the request's Accept-Language header decides, which
-- is what a first-time visitor should get.
ALTER TABLE users ADD COLUMN language TEXT NOT NULL DEFAULT '';
