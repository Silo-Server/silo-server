-- +goose NO TRANSACTION
-- +goose Up
-- The completed-only catalog index cannot serve unfinished history watches.
-- UTC makes the whole-second expression immutable and matches the v2 cursor.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_watch_history_item_witness
ON user_watch_history (user_id, profile_id, media_item_id,
    (date_trunc('second', watched_at AT TIME ZONE 'UTC')) DESC, id DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_user_watch_history_item_witness;
