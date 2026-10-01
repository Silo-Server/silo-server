-- +goose Up
ALTER TABLE public.download_artifacts
    VALIDATE CONSTRAINT download_artifacts_tone_map_recipe_check;

-- +goose Down
SELECT 1;
