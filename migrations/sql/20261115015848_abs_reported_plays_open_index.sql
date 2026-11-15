-- +goose NO TRANSACTION

-- +goose Up
-- One open reported play per client session, across every API server. Each
-- report finds its play through this index; only open reported plays are in
-- it, so it stays small.

-- A failed concurrent build can leave an invalid index. Keep retry cleanup
-- outside a transaction so it cannot take a blocking ordinary index-drop lock.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_abs_playback_sessions_open_reported;

CREATE UNIQUE INDEX CONCURRENTLY idx_abs_playback_sessions_open_reported
    ON public.abs_playback_sessions (user_id, profile_id, client_session_id)
    WHERE client_session_id IS NOT NULL AND closed_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_abs_playback_sessions_open_reported;
