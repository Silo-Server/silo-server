-- +goose Up
ALTER TABLE watch_provider_connections
    ADD COLUMN rate_limited_until TIMESTAMPTZ;

-- +goose Down
ALTER TABLE watch_provider_connections
    DROP COLUMN rate_limited_until;
