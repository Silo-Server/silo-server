-- +goose Up
-- A show's own episode order, set by an admin. provider: 'tvdb' or 'tmdb',
-- empty to follow the library. order_id: one of that provider's episode
-- orders (ListEpisodeOrders), empty for its default. file_order: the
-- provider whose default order the show's file names follow, empty for the
-- show's provider. layout: 'seasons' keeps the order's seasons, 'absolute'
-- numbers every regular episode in one season, 'custom' is reserved for
-- admin-arranged seasons.
CREATE TABLE public.series_episode_orders (
    series_id text PRIMARY KEY
        REFERENCES public.media_items (content_id) ON DELETE CASCADE ON UPDATE CASCADE,
    provider text NOT NULL DEFAULT '' CHECK (provider IN ('', 'tvdb', 'tmdb')),
    order_id text NOT NULL DEFAULT '',
    -- The order's name when it was chosen, for showing the choice without
    -- asking the provider.
    order_name text NOT NULL DEFAULT '',
    file_order text NOT NULL DEFAULT '' CHECK (file_order IN ('', 'tvdb', 'tmdb')),
    layout text NOT NULL DEFAULT 'seasons' CHECK (layout IN ('seasons', 'absolute', 'custom')),
    revision bigint NOT NULL DEFAULT 1,
    updated_by bigint REFERENCES public.users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (order_id = '' OR provider <> '')
);

-- The numbers a file name gives each episode, in the order the show's files
-- are named in. NULL means the episode's own numbers; -1 means the file
-- order lacks the episode, so no file name names it.
ALTER TABLE public.episodes
    ADD COLUMN file_season_number integer,
    ADD COLUMN file_episode_number integer;

-- +goose Down
ALTER TABLE public.episodes
    DROP COLUMN IF EXISTS file_season_number,
    DROP COLUMN IF EXISTS file_episode_number;
DROP TABLE IF EXISTS public.series_episode_orders;
