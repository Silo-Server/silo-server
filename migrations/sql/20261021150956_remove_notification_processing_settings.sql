-- +goose Up
-- +goose StatementBegin
-- The notification pipeline switches, staleness horizon, and retention
-- windows are fixed in code now; nothing reads these keys any more.
DELETE FROM server_settings
WHERE key IN (
    'notifications.release_events_enabled',
    'notifications.fanout_enabled',
    'notifications.fanout.max_event_age_hours',
    'notifications.retention.read_days',
    'notifications.retention.unread_days',
    'notifications.retention.event_days'
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The removed settings are not restored; their defaults apply again.
SELECT 1;
-- +goose StatementEnd
