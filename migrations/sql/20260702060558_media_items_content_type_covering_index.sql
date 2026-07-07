-- +goose NO TRANSACTION
-- +goose Up
-- Covering index for the ABS audiobook count/list path. The unfiltered
-- /api/libraries/{id}/items COUNT(*) seeks content_ids from the media_folder
-- index, then probed media_items by PK just to verify type='audiobook' — one
-- heap fetch per row (~255K probes, ~1M buffer hits, ~500ms). Adding type to
-- the content_id index lets that probe run index-only, cutting the count to
-- ~100ms. Also speeds the data query's per-row type check.
-- CONCURRENTLY avoids locking media_items writes during the build; it cannot
-- run inside a transaction, hence the NO TRANSACTION annotation above.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_content_type
ON public.media_items USING btree (content_id, type);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_content_type;
