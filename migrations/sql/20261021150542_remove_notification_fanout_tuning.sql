-- +goose Up
-- +goose StatementBegin
-- The settle window and per-series burst cap are gone: episodes fan out as
-- soon as they are recorded, and a fixed per-series push cooldown keeps a
-- season drop from pushing once per episode. Nothing reads these keys any
-- more.
DELETE FROM server_settings
WHERE key IN ('notifications.fanout.settle_seconds', 'notifications.fanout.max_series_burst');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The removed settings are not restored.
SELECT 1;
-- +goose StatementEnd
