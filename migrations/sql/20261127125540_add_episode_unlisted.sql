-- +goose Up
-- True for an episode the series' structure provider did not list in its
-- last full refresh: a row a renumber parked because the chosen order lacks
-- it. Bare episode numbers count through the series without these rows.
ALTER TABLE public.episodes
    ADD COLUMN unlisted boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE public.episodes
    DROP COLUMN IF EXISTS unlisted;
