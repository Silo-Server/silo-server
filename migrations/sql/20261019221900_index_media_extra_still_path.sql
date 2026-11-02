-- +goose NO TRANSACTION
-- +goose Up
-- The artwork revision GC and storage sweep check media_extras.still_path for
-- references (artworkReferenceUnionSQL). Like the other artwork path indexes
-- (20261011223117_index_item_extra_artwork_paths.sql), this one is built
-- CONCURRENTLY so a running server keeps writing, dropped first so a retry
-- after a failed build recovers, and partial on <> '' because most extras have
-- no image.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_extras_still_path_gc;
CREATE INDEX CONCURRENTLY idx_media_extras_still_path_gc ON public.media_extras (still_path) WHERE still_path <> '';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_extras_still_path_gc;
