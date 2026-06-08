-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    ADD COLUMN export_unwatched_enabled boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS export_unwatched_enabled;
-- +goose StatementEnd
