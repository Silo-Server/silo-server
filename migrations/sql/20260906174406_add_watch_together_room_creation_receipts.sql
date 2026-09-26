-- +goose Up
CREATE TABLE watch_together_room_creation_receipts (
    room_id TEXT PRIMARY KEY,
    host_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host_profile_id TEXT NOT NULL,
    selection_mode TEXT NOT NULL CHECK (selection_mode IN ('host_pick', 'vote')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- No room foreign key: deletion must not make a caller-selected identity reusable.
-- The receipt contains no code or invite token. Account deletion removes it.

-- +goose Down
DROP TABLE watch_together_room_creation_receipts;
