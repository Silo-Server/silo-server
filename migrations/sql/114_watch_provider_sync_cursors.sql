-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    ADD COLUMN sync_cursors jsonb NOT NULL DEFAULT '{}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    DROP COLUMN sync_cursors;
-- +goose StatementEnd
