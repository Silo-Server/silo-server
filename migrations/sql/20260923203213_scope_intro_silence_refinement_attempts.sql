-- +goose Up
-- chapters_hash: the refined intro segment comes from the file's chapters, which a
-- re-probe can change without touching the file identity or the stored marker.
-- recorded_by: a failure can be local to one server (missing ffmpeg, lost media
-- mount), so it only defers retries on the server that recorded it.
-- Existing rows get an empty chapters hash, which never matches, so those files
-- are refined once more.
ALTER TABLE intro_silence_refinement_attempts
    ADD COLUMN chapters_hash text NOT NULL DEFAULT '',
    ADD COLUMN recorded_by text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE intro_silence_refinement_attempts
    DROP COLUMN IF EXISTS recorded_by,
    DROP COLUMN IF EXISTS chapters_hash;
