-- +goose Up
-- +goose StatementBegin
-- Mobile push through the Silo relay now defaults to on for new installs, and
-- the setup wizard shows the disclosure before that default takes effect.
-- Servers that already have an account never saw that notice, so pin their
-- delivery toggles to the old default unless an admin already chose a value.
INSERT INTO server_settings (key, value)
SELECT keys.key, 'false'
FROM (VALUES
    ('notifications.apple_push_delivery_enabled'),
    ('notifications.android_push_delivery_enabled')
) AS keys(key)
WHERE EXISTS (SELECT 1 FROM users)
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally empty: the pinned rows are indistinguishable from an admin's
-- explicit choice, so they are left in place.
-- +goose StatementEnd
