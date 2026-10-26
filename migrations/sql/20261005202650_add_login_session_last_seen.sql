-- +goose Up
ALTER TABLE auth_sessions ADD COLUMN last_seen_at timestamptz;

-- +goose Down
ALTER TABLE auth_sessions DROP COLUMN last_seen_at;
