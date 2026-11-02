-- +goose NO TRANSACTION
-- +goose Up
-- The artwork revision GC and storage sweep check each media_items artwork
-- column for references (artworkReferenceUnionSQL); without an index that check
-- scans media_items once per column. These follow
-- 20260914145000_index_artwork_reference_paths.sql, built CONCURRENTLY so a
-- running server keeps writing and dropped first so a retry after a failed
-- build recovers. The columns are NOT NULL DEFAULT '' and most items have none
-- of these images, so the indexes are partial on <> '' to leave those rows out;
-- artworkReferenceUnionSQL repeats the predicate so the check can use them.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_banner_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_banner_path_gc ON public.media_items (banner_path) WHERE banner_path <> '';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_thumb_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_thumb_path_gc ON public.media_items (thumb_path) WHERE thumb_path <> '';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_clearart_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_clearart_path_gc ON public.media_items (clearart_path) WHERE clearart_path <> '';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_disc_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_disc_path_gc ON public.media_items (disc_path) WHERE disc_path <> '';

-- 20261011223116 added the widened cache-job checks NOT VALID. Validating
-- outside its transaction scans the queue without blocking its writers.
ALTER TABLE public.metadata_image_cache_jobs VALIDATE CONSTRAINT metadata_image_cache_jobs_target_check;
ALTER TABLE public.metadata_image_cache_jobs VALIDATE CONSTRAINT metadata_image_cache_jobs_image_type_check;
ALTER TABLE public.metadata_image_cache_jobs VALIDATE CONSTRAINT metadata_image_cache_jobs_shape_check;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_banner_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_thumb_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_clearart_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_disc_path_gc;
