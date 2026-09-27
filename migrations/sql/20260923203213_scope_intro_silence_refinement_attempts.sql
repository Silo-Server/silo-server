-- +goose Up
-- chapters_hash: the refined intro segment comes from the file's chapters, which a
-- re-probe can change without touching the file identity or the stored marker.
-- recorded_by: a failure can be local to one server (missing ffmpeg, lost media
-- mount), so it only defers retries on the server that recorded it.
-- media_file_id: media_files.id is bigint; databases that applied the first
-- version of the table migration created this column as integer. On a fresh
-- database it is already bigint and this is a no-op.
-- Existing rows get an empty chapters hash, which never matches, so those files
-- are refined once more.
ALTER TABLE intro_silence_refinement_attempts
    ALTER COLUMN media_file_id TYPE bigint,
    ADD COLUMN chapters_hash text NOT NULL DEFAULT '',
    ADD COLUMN recorded_by text NOT NULL DEFAULT '';

-- +goose Down
-- media_file_id stays bigint: the table migration now creates it that way.
ALTER TABLE intro_silence_refinement_attempts
    DROP COLUMN IF EXISTS recorded_by,
    DROP COLUMN IF EXISTS chapters_hash;
