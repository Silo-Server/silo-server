-- Intentionally a no-op migration.
--
-- The audiobooks absorption design spec assumed silo needed to add a
-- 'kind' column to a hypothetical media_libraries table to discriminate
-- between movies/tv/audiobooks/podcasts at the library level. Discovery
-- found that the actual table is media_folders and it already has a
-- type text NOT NULL column carrying that role (existing values:
-- 'movies', 'series', 'mixed').
--
-- There is no CHECK constraint or enum on media_folders.type, so adding
-- 'audiobooks' or 'podcasts' as future values requires no DDL — the
-- scanner branches in sub-plan 2 will simply read those values from
-- media_folders.type the same way they do for 'movies' and 'series'.
--
-- This migration is preserved as a numbered no-op so the audit trail
-- shows the decision rather than leaving a numbering gap. The matching
-- .down.sql is also a no-op.

SELECT 1;
