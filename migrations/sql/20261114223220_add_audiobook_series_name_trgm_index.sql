-- +goose NO TRANSACTION
-- +goose Up
-- The Audiobookshelf search endpoint matches series names by substring
-- (series_name ILIKE '%term%'). The existing audiobook_series_name_lower index
-- serves only exact matches, so the search scanned every membership row; a
-- trigram index lets it read just the matching ones, as idx_people_name_trgm
-- does for the author arm.
--
-- CONCURRENTLY keeps audiobook_series writable during the build; it cannot run
-- inside a transaction, hence the NO TRANSACTION annotation above.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audiobook_series_name_trgm
ON public.audiobook_series USING gin (series_name public.gin_trgm_ops);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_audiobook_series_name_trgm;
