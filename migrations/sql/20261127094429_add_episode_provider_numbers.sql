-- +goose Up
-- The place an episode has in its provider's default order, for services
-- that address episodes by season and number (watch sync, intro markers),
-- when the series shows another order. provider_numbering is the provider
-- ('tvdb' or 'tmdb'); NULL numbers mean the episode's own numbers are that
-- order's.
ALTER TABLE public.episodes
    ADD COLUMN provider_numbering text,
    ADD COLUMN provider_season_number integer,
    ADD COLUMN provider_episode_number integer;

-- +goose Down
ALTER TABLE public.episodes
    DROP COLUMN IF EXISTS provider_numbering,
    DROP COLUMN IF EXISTS provider_season_number,
    DROP COLUMN IF EXISTS provider_episode_number;
