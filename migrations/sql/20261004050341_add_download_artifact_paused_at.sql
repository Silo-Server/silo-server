-- +goose Up
-- An administrator can pause an unfinished prepare job. A paused job keeps its
-- queued status (and its place in claim order) but is never claimed until it
-- is resumed. A separate column leaves the recipe status families untouched.
ALTER TABLE public.download_artifacts
    ADD COLUMN paused_at timestamptz;

-- +goose Down
ALTER TABLE public.download_artifacts
    DROP COLUMN paused_at;
