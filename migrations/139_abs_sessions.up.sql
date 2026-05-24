-- Audiobookshelf-compatible client sessions. Parallel to
-- jellycompat_sessions: each row identifies an ABS mobile/desktop
-- client by its device + token so it can reconnect without
-- re-authenticating against silo's main auth_sessions.

CREATE TABLE IF NOT EXISTS public.abs_sessions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    token        TEXT NOT NULL,
    device_id    TEXT NOT NULL,
    device_name  TEXT,
    client_name  TEXT,
    client_version TEXT,
    created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    revoked_at   TIMESTAMP WITH TIME ZONE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_abs_sessions_token
    ON public.abs_sessions (token);

CREATE INDEX IF NOT EXISTS idx_abs_sessions_user_device
    ON public.abs_sessions (user_id, device_id);

CREATE INDEX IF NOT EXISTS idx_abs_sessions_last_seen
    ON public.abs_sessions (last_seen_at);
