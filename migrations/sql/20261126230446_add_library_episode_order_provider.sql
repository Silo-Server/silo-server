-- +goose Up
-- The metadata provider whose episode order a TV library follows: 'tvdb' or
-- 'tmdb'. Empty means the first enabled remote provider in the library's
-- Episodes chain, which is what every library followed before this column.
ALTER TABLE public.media_folders
    ADD COLUMN episode_order_provider text NOT NULL DEFAULT '',
    ADD CONSTRAINT media_folders_episode_order_provider_check
        CHECK (episode_order_provider IN ('', 'tvdb', 'tmdb'));

-- +goose Down
ALTER TABLE public.media_folders
    DROP CONSTRAINT IF EXISTS media_folders_episode_order_provider_check,
    DROP COLUMN IF EXISTS episode_order_provider;
