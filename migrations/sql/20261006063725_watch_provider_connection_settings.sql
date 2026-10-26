-- +goose Up
-- +goose StatementBegin

-- Values for the settings a watch-provider plugin declares for each
-- connection, such as whether Simkl logs rewatches. Keys are the plugin's
-- setting keys and values are strings in the plugin contract's encoding
-- ("true" or "false" for a switch). A setting the profile has not changed is
-- absent and takes the plugin's declared default.
ALTER TABLE public.watch_provider_connections
    ADD COLUMN connection_settings jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS connection_settings;
-- +goose StatementEnd
