-- +goose NO TRANSACTION
-- +goose Up
-- Serve live-session keyset pages in account order without sorting every session.
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
          AND c.relname = 'idx_auth_sessions_user_created'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_auth_sessions_user_created;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_sessions_user_created
ON auth_sessions (user_id, created_at DESC, id DESC)
WHERE revoked_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_auth_sessions_user_created;
