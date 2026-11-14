-- +goose NO TRANSACTION
-- +goose Up
-- The artwork revision GC and storage sweep check seasons.backdrop_path for
-- references (artworkReferenceUnionSQL). Most seasons have no backdrop, so the
-- index is partial on <> '', built CONCURRENTLY and dropped first so a retry
-- after a failed build recovers, as 20261011223117_index_item_extra_artwork_paths.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_seasons_backdrop_path_gc;
CREATE INDEX CONCURRENTLY idx_seasons_backdrop_path_gc ON public.seasons (backdrop_path) WHERE backdrop_path <> '';

-- 20261028150252 added the widened cache-job shape check NOT VALID.
-- Validating outside its transaction scans the queue without blocking its
-- writers.
ALTER TABLE public.metadata_image_cache_jobs VALIDATE CONSTRAINT metadata_image_cache_jobs_shape_check;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_seasons_backdrop_path_gc;
