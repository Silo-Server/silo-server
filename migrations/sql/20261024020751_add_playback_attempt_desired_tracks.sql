-- +goose Up
-- The audio and subtitle the viewer asked for in this attempt, kept apart from
-- what the current plan could deliver, so a version change that loses a track
-- can restore it later. NULL for attempts written before this column existed.
ALTER TABLE playback_v3_attempts ADD COLUMN desired_tracks JSONB;

-- +goose Down
ALTER TABLE playback_v3_attempts DROP COLUMN desired_tracks;
