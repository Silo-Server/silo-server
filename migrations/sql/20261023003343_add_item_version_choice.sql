-- +goose Up
-- A row keyed by a movie's own content ID records the exact version the
-- profile last started, so the choice survives a play too short to leave a
-- progress row. Series rows keep only resolution/HDR/codec and leave these
-- NULL: one file cannot stand for every episode.
ALTER TABLE user_series_playback_preferences
    ADD COLUMN IF NOT EXISTS file_id BIGINT NULL,
    ADD COLUMN IF NOT EXISTS edition_key TEXT NULL;

-- +goose Down
ALTER TABLE user_series_playback_preferences
    DROP COLUMN IF EXISTS edition_key,
    DROP COLUMN IF EXISTS file_id;
