-- +goose NO TRANSACTION
-- +goose Up
-- Match the account equality prefix and both v2 keyset ordering columns.
-- Remove an unusable remnant of an interrupted concurrent build before retry.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_media_requests_page_order'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_media_requests_page_order;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_requests_page_order
ON media_requests (requested_by_user_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_media_requests_page_order;
