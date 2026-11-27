-- +goose Up
-- Admin-arranged seasons of a series whose episode order uses the 'custom'
-- layout. number is the season's number; empty title and overview take the
-- provider season with the same number's.
CREATE TABLE public.series_custom_seasons (
    series_id text NOT NULL
        REFERENCES public.series_episode_orders (series_id) ON DELETE CASCADE ON UPDATE CASCADE,
    number integer NOT NULL CHECK (number >= 1),
    title text NOT NULL DEFAULT '',
    overview text NOT NULL DEFAULT '',
    -- An uploaded poster (artwork storage path, its upload:// source and
    -- thumbhash); empty takes the provider season with the same number's.
    poster_path text NOT NULL DEFAULT '',
    poster_source text NOT NULL DEFAULT '',
    poster_thumbhash text NOT NULL DEFAULT '',
    PRIMARY KEY (series_id, number)
);

-- Where each episode of an arranged series sits. episode_key is
-- '<provider>:<provider episode id>' of the series' structure provider, the
-- same in every order of that provider. season 0 is Specials.
CREATE TABLE public.series_custom_episode_slots (
    series_id text NOT NULL
        REFERENCES public.series_episode_orders (series_id) ON DELETE CASCADE ON UPDATE CASCADE,
    episode_key text NOT NULL,
    season integer NOT NULL CHECK (season >= 0),
    position integer NOT NULL CHECK (position >= 1),
    PRIMARY KEY (series_id, episode_key),
    UNIQUE (series_id, season, position)
);

-- The order an arrangement was made from, so it reopens on that order while
-- the series shows another layout.
ALTER TABLE public.series_episode_orders
    ADD COLUMN custom_provider text NOT NULL DEFAULT '',
    ADD COLUMN custom_order_id text NOT NULL DEFAULT '',
    ADD COLUMN custom_file_order text NOT NULL DEFAULT '';

-- Posters uploaded for a series' arranged seasons. A save names an upload by
-- its source; only these are accepted, and a season that drops one gets the
-- provider's poster back.
CREATE TABLE public.series_custom_season_uploads (
    series_id text NOT NULL
        REFERENCES public.media_items (content_id) ON DELETE CASCADE ON UPDATE CASCADE,
    source text NOT NULL,
    path text NOT NULL,
    thumbhash text NOT NULL DEFAULT '',
    uploaded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (series_id, source)
);

-- +goose Down
DROP TABLE IF EXISTS public.series_custom_season_uploads;
ALTER TABLE public.series_episode_orders
    DROP COLUMN IF EXISTS custom_provider,
    DROP COLUMN IF EXISTS custom_order_id,
    DROP COLUMN IF EXISTS custom_file_order;
DROP TABLE IF EXISTS public.series_custom_episode_slots;
DROP TABLE IF EXISTS public.series_custom_seasons;
