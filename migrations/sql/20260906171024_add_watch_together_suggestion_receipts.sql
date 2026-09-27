-- +goose Up
CREATE TABLE watch_together_suggestion_receipts (
    suggestion_id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    room_id TEXT NOT NULL,
    request_digest BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- Receipts intentionally survive suggestion and room deletion. They contain no
-- title, note or poster URL and prevent reusing a deleted suggestion identity.

-- +goose Down
DROP TABLE watch_together_suggestion_receipts;
