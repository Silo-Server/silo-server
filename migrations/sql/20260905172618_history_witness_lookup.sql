-- +goose NO TRANSACTION
-- +goose Up
-- The completed-only catalog index cannot serve unfinished history watches.
-- UTC makes the whole-second expression immutable and matches the v2 cursor.
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
          AND c.relname = 'idx_user_watch_history_item_witness'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_user_watch_history_item_witness;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_watch_history_item_witness
ON user_watch_history (user_id, profile_id, media_item_id,
    (date_trunc('second', watched_at AT TIME ZONE 'UTC')) DESC, id DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_user_watch_history_item_witness;
