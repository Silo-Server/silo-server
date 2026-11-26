-- +goose NO TRANSACTION
-- +goose Up
-- Admin Insights' Library tab finds titles added within a time window across
-- all libraries by pre-filtering candidate content_ids on first_seen_at before
-- taking MIN(first_seen_at) per title. Existing media_item_libraries indexes
-- lead with media_folder_id or content_id, so unfiltered reads had to scan the
-- whole table. (first_seen_at, content_id) serves that range as an index-only
-- scan.
--
-- CONCURRENTLY avoids write locks on a running server and requires
-- NO TRANSACTION. Drop first so a retry after an interrupted build does not
-- keep an invalid index behind.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_item_libraries_first_seen_content;
CREATE INDEX CONCURRENTLY idx_media_item_libraries_first_seen_content
    ON public.media_item_libraries (first_seen_at, content_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_item_libraries_first_seen_content;
