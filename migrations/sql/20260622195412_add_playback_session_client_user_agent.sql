-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS client_name text,
    ADD COLUMN IF NOT EXISTS client_version text,
    ADD COLUMN IF NOT EXISTS client_user_agent text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN IF EXISTS client_user_agent,
    DROP COLUMN IF EXISTS client_version,
    DROP COLUMN IF EXISTS client_name;
-- +goose StatementEnd
