-- +goose Up
-- The refined intro segment comes from the file's chapters, which a re-probe can
-- change without touching the file identity or the stored marker. Existing rows
-- get an empty hash, which never matches, so those files are refined once more.
ALTER TABLE intro_silence_refinement_attempts
    ADD COLUMN chapters_hash text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE intro_silence_refinement_attempts
    DROP COLUMN IF EXISTS chapters_hash;
