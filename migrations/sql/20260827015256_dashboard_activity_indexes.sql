-- +goose NO TRANSACTION
-- +goose Up
-- The admin dashboard's activity aggregates scan by start/watch time over a
-- rolling window. Both tables are only indexed on their end-of-play timestamps
-- today (idx_playback_history_admin_ended, idx_user_watch_history_*), which a
-- "started in the last N hours" or "watched in the last N days" filter cannot
-- use.
--
-- Both tables grow with every play on a busy deployment, so the indexes are
-- built CONCURRENTLY and this migration runs outside a transaction: a plain
-- CREATE INDEX would hold a write lock for the length of the build.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_playback_history_admin_started'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_playback_history_admin_started;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_user_watch_history_watched_at'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_user_watch_history_watched_at;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_playback_history_admin_started
    ON public.playback_history_admin USING btree (started_at DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_watch_history_watched_at
    ON public.user_watch_history USING btree (watched_at DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_user_watch_history_watched_at;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_playback_history_admin_started;
