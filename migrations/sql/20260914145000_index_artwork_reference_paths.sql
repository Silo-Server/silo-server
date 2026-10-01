-- +goose NO TRANSACTION
-- +goose Up
-- The artwork revision GC asks "does any catalog surface still reference this
-- path?" before deleting an object. artworkReferenceUnionSQL builds that check
-- as a UNION ALL over every sweep surface, filtering each on its pathCol.
--
-- None of those columns is indexed, so the check sequentially scans every
-- surface. On the deployment this was measured on: episodes 2.2M rows, people
-- 1.2M, media_items 625k (scanned three times -- poster, backdrop, logo) and
-- seasons 139k. A batched check over 10,000 candidate paths ran for more than
-- ten minutes; with these indexes the same check takes 0.5-2.1s.
--
-- media_item_localizations, season_localizations and library_collections are
-- deliberately left unindexed: they hold 0, 0 and 159 rows respectively, where
-- a sequential scan is cheaper than an index probe and an index would only add
-- write overhead.
--
-- Partial on IS NOT NULL: a row with no artwork can never match a candidate
-- path, and excluding those keeps the indexes small.
--
-- CONCURRENTLY so this does not take a write lock on a running server, which
-- requires NO TRANSACTION. Note that CREATE INDEX CONCURRENTLY waits for every
-- in-flight transaction to commit, so a long-running GC pass blocks it until
-- that pass finishes.
--
-- A failed nontransactional run can leave completed or invalid indexes behind.
-- Drop each one concurrently before rebuilding so retries recover without a
-- write-blocking ordinary DROP INDEX. A retry rebuilds completed indexes too.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_poster_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_poster_path_gc ON public.media_items (poster_path) WHERE poster_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_backdrop_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_backdrop_path_gc ON public.media_items (backdrop_path) WHERE backdrop_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_logo_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_logo_path_gc ON public.media_items (logo_path) WHERE logo_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_still_path_gc;
CREATE INDEX CONCURRENTLY idx_episodes_still_path_gc ON public.episodes (still_path) WHERE still_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_photo_path_gc;
CREATE INDEX CONCURRENTLY idx_people_photo_path_gc ON public.people (photo_path) WHERE photo_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_seasons_poster_path_gc;
CREATE INDEX CONCURRENTLY idx_seasons_poster_path_gc ON public.seasons (poster_path) WHERE poster_path IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_poster_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_backdrop_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_logo_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_still_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_photo_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_seasons_poster_path_gc;
