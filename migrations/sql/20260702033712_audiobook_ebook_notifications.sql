-- +goose Up
-- +goose StatementBegin
-- Audiobook and ebook "recently added" notifications. Both kinds are flat
-- item kinds like movies (item_id only, no series/episode columns): they
-- widen the existing item-shaped branch of the release_events constraints
-- and get their own server-channel toggles.

ALTER TABLE public.release_events
    DROP CONSTRAINT release_events_kind_check,
    DROP CONSTRAINT release_events_kind_shape_check;
ALTER TABLE public.release_events
    ADD CONSTRAINT release_events_kind_check
        CHECK (kind IN ('episode', 'movie', 'audiobook', 'ebook')),
    ADD CONSTRAINT release_events_kind_shape_check CHECK (
        (kind = 'episode' AND series_id IS NOT NULL AND episode_id IS NOT NULL
            AND season_number IS NOT NULL AND episode_number IS NOT NULL
            AND episode_key IS NOT NULL)
        OR (kind IN ('movie', 'audiobook', 'ebook') AND item_id IS NOT NULL)
    );

-- Generic flat-item availability facts for every kind after movie: one-way
-- "item first became available in this library" rows, movie_availability's
-- shape plus a kind discriminator. Movies deliberately stay in their original
-- table (renaming a populated hot-path table buys nothing); a future kind
-- widens the kind CHECKs here and on release_events, no new table.
CREATE TABLE public.item_availability (
    library_id integer NOT NULL,
    item_id text NOT NULL,
    kind text NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT item_availability_pkey PRIMARY KEY (library_id, item_id, kind),
    CONSTRAINT item_availability_kind_check CHECK (kind IN ('audiobook', 'ebook'))
);

-- Per-channel content toggles for the new kinds, defaulting on like the
-- movie/episode toggles. No back-catalog flood risk: existing audiobook and
-- ebook libraries have no notification_content_seed_state rows for these
-- kinds yet, so their first full scan seeds silently.
ALTER TABLE public.notification_server_channels
    ADD COLUMN notify_new_audiobooks boolean NOT NULL DEFAULT true,
    ADD COLUMN notify_new_ebooks boolean NOT NULL DEFAULT true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.notification_server_channels
    DROP COLUMN IF EXISTS notify_new_ebooks,
    DROP COLUMN IF EXISTS notify_new_audiobooks;

DROP TABLE IF EXISTS public.item_availability;

DELETE FROM public.release_events WHERE kind IN ('audiobook', 'ebook');
ALTER TABLE public.release_events
    DROP CONSTRAINT IF EXISTS release_events_kind_shape_check,
    DROP CONSTRAINT IF EXISTS release_events_kind_check;
ALTER TABLE public.release_events
    ADD CONSTRAINT release_events_kind_check CHECK (kind IN ('episode', 'movie')),
    ADD CONSTRAINT release_events_kind_shape_check CHECK (
        (kind = 'episode' AND series_id IS NOT NULL AND episode_id IS NOT NULL
            AND season_number IS NOT NULL AND episode_number IS NOT NULL
            AND episode_key IS NOT NULL)
        OR (kind = 'movie' AND item_id IS NOT NULL)
    );
-- +goose StatementEnd
