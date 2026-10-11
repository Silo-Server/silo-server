-- +goose NO TRANSACTION
-- +goose Up
-- The artwork revision GC and storage sweep check each media_items artwork
-- column for references (artworkReferenceUnionSQL); without an index that check
-- scans media_items once per column. These follow
-- 20260914145000_index_artwork_reference_paths.sql: partial on IS NOT NULL so a
-- reference check on "= ANY($1)" can use them, built CONCURRENTLY so a running
-- server keeps writing, and dropped first so a retry after a failed build
-- recovers.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_banner_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_banner_path_gc ON public.media_items (banner_path) WHERE banner_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_thumb_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_thumb_path_gc ON public.media_items (thumb_path) WHERE thumb_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_clearart_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_clearart_path_gc ON public.media_items (clearart_path) WHERE clearart_path IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_disc_path_gc;
CREATE INDEX CONCURRENTLY idx_media_items_disc_path_gc ON public.media_items (disc_path) WHERE disc_path IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_banner_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_thumb_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_clearart_path_gc;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_disc_path_gc;
