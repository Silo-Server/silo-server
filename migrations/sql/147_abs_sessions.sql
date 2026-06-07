-- +goose Up
-- +goose StatementBegin
-- Audiobookshelf-compatible client sessions. Same role as
-- jellycompat_sessions (compat-layer token store) but with a proper
-- users FK, device metadata, and soft revocation via revoked_at instead
-- of hard expiry.

CREATE TABLE IF NOT EXISTS public.abs_sessions (
    id           bigserial PRIMARY KEY,
    user_id      integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    token_hash   text NOT NULL,
    device_id    text NOT NULL,
    device_name  text,
    client_name  text,
    client_version  text,
    created_at   timestamp with time zone NOT NULL DEFAULT now(),
    last_seen_at timestamp with time zone NOT NULL DEFAULT now(),
    revoked_at   timestamp with time zone
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_abs_sessions_token_hash
    ON public.abs_sessions (token_hash);

CREATE INDEX IF NOT EXISTS idx_abs_sessions_user_device
    ON public.abs_sessions (user_id, device_id);

CREATE INDEX IF NOT EXISTS idx_abs_sessions_last_seen
    ON public.abs_sessions (last_seen_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_abs_sessions_last_seen;
DROP INDEX IF EXISTS public.idx_abs_sessions_user_device;
DROP INDEX IF EXISTS public.idx_abs_sessions_token_hash;
DROP TABLE IF EXISTS public.abs_sessions;
-- +goose StatementEnd
