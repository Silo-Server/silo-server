-- +goose Up
-- +goose StatementBegin
ALTER TABLE watch_together_rooms
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private',
    ADD CONSTRAINT watch_together_rooms_visibility_check
        CHECK (visibility IN ('private', 'public'));

CREATE INDEX idx_watch_together_rooms_public_open
    ON watch_together_rooms (created_at DESC)
    WHERE visibility = 'public' AND phase <> 'ended';

-- One request per profile per room. A denied request may be asked again,
-- which moves the same row back to pending.
CREATE TABLE watch_together_join_requests (
    id           TEXT        PRIMARY KEY,
    room_id      TEXT        NOT NULL REFERENCES watch_together_rooms(id) ON DELETE CASCADE,
    user_id      INTEGER     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id   TEXT        NOT NULL,
    display_name TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at   TIMESTAMPTZ,
    CONSTRAINT watch_together_join_requests_status_check
        CHECK (status IN ('pending', 'approved', 'denied')),
    CONSTRAINT watch_together_join_requests_requester_key
        UNIQUE (room_id, user_id, profile_id)
);

CREATE INDEX idx_watch_together_join_requests_user
    ON watch_together_join_requests (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS watch_together_join_requests;
DROP INDEX IF EXISTS idx_watch_together_rooms_public_open;
ALTER TABLE watch_together_rooms
    DROP CONSTRAINT IF EXISTS watch_together_rooms_visibility_check,
    DROP COLUMN IF EXISTS visibility;
-- +goose StatementEnd
