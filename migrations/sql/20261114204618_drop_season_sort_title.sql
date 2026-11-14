-- +goose Up
-- Seasons order by season number everywhere, so a season sort title would do
-- nothing. Episodes keep theirs.
ALTER TABLE public.seasons
    DROP COLUMN IF EXISTS sort_title;

-- +goose Down
ALTER TABLE public.seasons
    ADD COLUMN IF NOT EXISTS sort_title text DEFAULT ''::text NOT NULL;
