-- +goose NO TRANSACTION
-- +goose Up
-- Targeted indexes for Insights custom-range queries, item-scoped history
-- lookups, and O(1) oldest-coverage lookups on admin_playback_history.
-- idx_admin_playback_history_started already covers started_at DESC scans.
--
-- CONCURRENTLY avoids write locks on a running server and requires
-- NO TRANSACTION. Drop any invalid leftover index first so retries rebuild it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_admin_playback_history_item_ended'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_admin_playback_history_item_ended;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_admin_playback_history_route_facts_started'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_admin_playback_history_route_facts_started;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_admin_playback_history_measured_started'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_admin_playback_history_measured_started;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_admin_playback_history_item_ended
    ON public.admin_playback_history USING btree (media_item_id, ended_at DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_admin_playback_history_route_facts_started
    ON public.admin_playback_history USING btree (started_at ASC)
    WHERE route_facts_recorded;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_admin_playback_history_measured_started
    ON public.admin_playback_history USING btree (started_at ASC)
    WHERE measured;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_admin_playback_history_measured_started;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_admin_playback_history_route_facts_started;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_admin_playback_history_item_ended;
