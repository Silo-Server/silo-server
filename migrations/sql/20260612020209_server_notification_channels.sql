-- +goose Up
-- +goose StatementBegin
-- Admin server notification channels ("community channels") + movie event
-- spine. Server channels are admin-owned outbound webhooks fed straight from
-- release_events via a per-channel watermark sweep (no per-profile fanout),
-- announcing newly added movies/episodes and media request lifecycle events.

-- 1) release_events gains a kind discriminator so movie events share the
-- table (the dedupe_key column was designed for exactly this). Episode rows
-- keep their shape; movie rows carry item_id (media_items.content_id) with
-- dedupe keys in a distinct "movie:{library_id}:{item_id}" keyspace.
ALTER TABLE public.release_events
    ADD COLUMN kind text NOT NULL DEFAULT 'episode',
    ADD COLUMN item_id text;
ALTER TABLE public.release_events
    ALTER COLUMN series_id DROP NOT NULL,
    ALTER COLUMN episode_id DROP NOT NULL,
    ALTER COLUMN season_number DROP NOT NULL,
    ALTER COLUMN episode_number DROP NOT NULL,
    ALTER COLUMN episode_key DROP NOT NULL;
ALTER TABLE public.release_events
    ADD CONSTRAINT release_events_kind_check CHECK (kind IN ('episode', 'movie')),
    ADD CONSTRAINT release_events_kind_shape_check CHECK (
        (kind = 'episode' AND series_id IS NOT NULL AND episode_id IS NOT NULL
            AND season_number IS NOT NULL AND episode_number IS NOT NULL
            AND episode_key IS NOT NULL)
        OR (kind = 'movie' AND item_id IS NOT NULL)
    );

-- Server-channel sweep cursor: strictly increasing (created_at, id) walk.
CREATE INDEX release_events_sweep_idx
    ON public.release_events (created_at, id);

-- 2) Movie availability facts: one-way "movie first became available in this
-- library" rows, mirror of episode_availability. Persist across file churn so
-- re-added files never re-notify.
CREATE TABLE public.movie_availability (
    library_id integer NOT NULL,
    item_id text NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT movie_availability_pkey PRIMARY KEY (library_id, item_id)
);

-- 3) Per-kind seed markers for every content kind after episodes (movies
-- now; audiobooks/music later need only a new kind value). Deliberately
-- separate from notification_library_seed_state: that table was already
-- seed-marked for every scanned library (including movie libraries) by the
-- episode-only seeding pass, with zero movie availability rows. Reusing it
-- would emit release events for the entire movie back catalog on the first
-- post-upgrade scan. A kind seeds silently until a row lands here.
CREATE TABLE public.notification_content_seed_state (
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    kind text NOT NULL,
    seeded_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_content_seed_state_pkey PRIMARY KEY (library_id, kind)
);

-- 4) Admin server channels. url/signing_secret ciphertexts hold enc:v1:
-- envelopes (internal/secret) with channel-scoped AADs distinct from the
-- profile webhook AAD namespace. created_by_user_id has no FK by convention
-- (informational only). The watermark defaults to creation time so a new
-- channel never replays history; re-enabling a disabled channel fast-forwards
-- it to now() in code.
CREATE TABLE public.notification_server_channels (
    id text PRIMARY KEY,
    name varchar(64) NOT NULL,
    type text NOT NULL,
    url_ciphertext text NOT NULL,
    url_host varchar(253) NOT NULL,
    signing_secret_ciphertext text,
    enabled boolean NOT NULL DEFAULT true,
    notify_new_movies boolean NOT NULL DEFAULT true,
    notify_new_episodes boolean NOT NULL DEFAULT true,
    notify_request_submitted boolean NOT NULL DEFAULT false,
    notify_request_approved boolean NOT NULL DEFAULT false,
    notify_request_declined boolean NOT NULL DEFAULT false,
    notify_request_fulfilled boolean NOT NULL DEFAULT false,
    watermark_created_at timestamptz NOT NULL DEFAULT now(),
    watermark_id text NOT NULL DEFAULT '',
    last_attempt_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,
    disabled_reason varchar(256),
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_failure_status integer,
    last_failure_message varchar(256),
    created_by_user_id integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_server_channels_name_key UNIQUE (name),
    CONSTRAINT notification_server_channels_type_check CHECK (type IN ('discord', 'generic')),
    CONSTRAINT notification_server_channels_secret_check
        CHECK (type = 'discord' OR signing_secret_ciphertext IS NOT NULL)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.notification_server_channels;
DROP TABLE IF EXISTS public.notification_content_seed_state;
DROP TABLE IF EXISTS public.movie_availability;
DROP INDEX IF EXISTS public.release_events_sweep_idx;
DELETE FROM public.release_events WHERE kind <> 'episode';
ALTER TABLE public.release_events
    DROP CONSTRAINT IF EXISTS release_events_kind_shape_check,
    DROP CONSTRAINT IF EXISTS release_events_kind_check;
ALTER TABLE public.release_events
    ALTER COLUMN series_id SET NOT NULL,
    ALTER COLUMN episode_id SET NOT NULL,
    ALTER COLUMN season_number SET NOT NULL,
    ALTER COLUMN episode_number SET NOT NULL,
    ALTER COLUMN episode_key SET NOT NULL;
ALTER TABLE public.release_events
    DROP COLUMN IF EXISTS item_id,
    DROP COLUMN IF EXISTS kind;
-- +goose StatementEnd
