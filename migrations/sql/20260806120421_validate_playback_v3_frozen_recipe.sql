-- +goose Up
ALTER TABLE playback_v3_attempts
    VALIDATE CONSTRAINT playback_v3_attempts_frozen_recipe_object;

-- +goose Down
-- PostgreSQL cannot mark a validated CHECK constraint NOT VALID again. The
-- preceding migration's Down step drops the constraint with its column.
SELECT 1;
