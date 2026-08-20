-- +goose NO TRANSACTION

-- +goose Up
-- A failed concurrent build can leave an invalid index that causes
-- IF NOT EXISTS to skip every retry. Remove only that unusable artifact.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'watch_provider_scrobble_reconcile_pending_idx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.watch_provider_scrobble_reconcile_pending_idx;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS watch_provider_scrobble_reconcile_pending_idx
    ON public.watch_provider_scrobble_sessions (stop_sent_at)
    WHERE stop_sent_at IS NOT NULL
      AND completed = true
      AND history_id <> ''
      AND history_reconciled_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.watch_provider_scrobble_reconcile_pending_idx;
