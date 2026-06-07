-- +goose Up
-- +goose StatementBegin
INSERT INTO server_settings (key, value)
VALUES ('scanner.empty_trash_after_scan', 'true')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM server_settings WHERE key = 'scanner.empty_trash_after_scan';
-- +goose StatementEnd
