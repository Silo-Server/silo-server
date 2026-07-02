-- +goose NO TRANSACTION

-- +goose Up
-- Semantic vector "coverage" counts filter media_item_embeddings by the active
-- embedding model (e.model = $2). Without an index on model that filter scans
-- the whole embeddings table on every coverage/status refresh. A leftover
-- INVALID index can survive a crashed CREATE INDEX CONCURRENTLY, which then
-- blocks the IF NOT EXISTS retry below; drop it first.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_media_item_embeddings_model'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_media_item_embeddings_model;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_item_embeddings_model
ON public.media_item_embeddings (model);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_media_item_embeddings_model;
