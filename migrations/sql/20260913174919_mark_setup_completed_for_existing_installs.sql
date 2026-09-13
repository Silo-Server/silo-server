-- +goose Up
-- +goose StatementBegin
-- The setup wizard now records when it was finished so the /setup route can
-- refuse to run a second time. Servers that already have an account finished
-- (or deliberately abandoned) setup on an older build that never wrote the
-- marker, so treat them as complete rather than reopening the wizard for them.
INSERT INTO server_settings (key, value)
SELECT 'setup.completed', 'true'
WHERE EXISTS (SELECT 1 FROM users)
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally empty: a backfilled row is indistinguishable from one the
-- wizard or an admin wrote, so it is left in place.
-- +goose StatementEnd
