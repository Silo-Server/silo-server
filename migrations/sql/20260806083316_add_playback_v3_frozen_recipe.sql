-- +goose Up
ALTER TABLE playback_v3_attempts
    ADD COLUMN frozen_recipe JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE playback_v3_attempts
    ADD CONSTRAINT playback_v3_attempts_frozen_recipe_object
        CHECK (jsonb_typeof(frozen_recipe) = 'object') NOT VALID;

-- +goose Down
ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT IF EXISTS playback_v3_attempts_frozen_recipe_object,
    DROP COLUMN IF EXISTS frozen_recipe;
