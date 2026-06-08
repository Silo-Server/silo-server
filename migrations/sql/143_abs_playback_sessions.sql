-- +goose Up
-- +goose StatementBegin
-- Active Audiobookshelf-compatible play sessions. One row per
-- /abs/api/items/{id}/play that hasn't yet been closed via
-- /abs/api/session/{sid}/close. Used to track per-session listening
-- time for ABS apps' "time listened today" stats.

CREATE TABLE IF NOT EXISTS public.abs_playback_sessions (
    id                         TEXT PRIMARY KEY,                   -- ULID issued at play start
    user_id                    INTEGER NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id                 TEXT NOT NULL,
    content_id                 TEXT NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    media_file_id              INTEGER REFERENCES public.media_files(id) ON DELETE SET NULL,
    started_at                 TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    last_sync_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    closed_at                  TIMESTAMP WITH TIME ZONE,
    time_listening_seconds     INTEGER NOT NULL DEFAULT 0,
    current_position_seconds   DOUBLE PRECISION NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_abs_playback_sessions_user_profile
    ON public.abs_playback_sessions (user_id, profile_id);

CREATE INDEX IF NOT EXISTS idx_abs_playback_sessions_open
    ON public.abs_playback_sessions (closed_at)
    WHERE closed_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_abs_playback_sessions_open;
DROP INDEX IF EXISTS public.idx_abs_playback_sessions_user_profile;
DROP TABLE IF EXISTS public.abs_playback_sessions;
-- +goose StatementEnd
