-- +goose Up
-- +goose StatementBegin
-- Foundation schema for profile-scoped release notifications
-- (docs/superpowers/plans/notifications/01-release-events-and-inbox.md).
--
-- episode_availability is a one-way "episode first became available in this
-- library" fact. Rows are inserted by live ingest and by silent seeding
-- (initial library scans, feature-enable backfill); they persist across file
-- churn so re-added files never re-notify.
CREATE TABLE public.episode_availability (
    library_id integer NOT NULL,
    episode_id text NOT NULL,
    series_id text NOT NULL,
    season_number integer NOT NULL,
    episode_number integer NOT NULL,
    episode_key integer NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT episode_availability_pkey PRIMARY KEY (library_id, episode_id)
);

CREATE INDEX episode_availability_series_idx
    ON public.episode_availability (library_id, series_id, episode_key DESC);

-- Per-library marker that availability seeding completed. Release events are
-- emitted only for libraries with a row here; unseeded libraries insert
-- availability silently ("newly available" means newly released to this
-- server, not newly seen by the notifications feature).
CREATE TABLE public.notification_library_seed_state (
    library_id integer PRIMARY KEY REFERENCES public.media_folders(id) ON DELETE CASCADE,
    seeded_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.release_events (
    id text PRIMARY KEY,
    library_id integer NOT NULL,
    series_id text NOT NULL,
    episode_id text NOT NULL,
    season_number integer NOT NULL,
    episode_number integer NOT NULL,
    episode_key integer NOT NULL,
    available_at timestamptz NOT NULL,
    -- Explicit column (rather than a composite unique) so future event kinds
    -- can share the table with their own key shapes. Composed as
    -- "{library_id}:{episode_id}".
    dedupe_key text NOT NULL,
    processed_at timestamptz,
    -- NULL for fanned-out events; 'series_burst' when the per-series burst
    -- cap consumed this event without fanout.
    suppressed_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT release_events_dedupe_key_key UNIQUE (dedupe_key)
);

CREATE INDEX release_events_unprocessed_idx
    ON public.release_events (processed_at, created_at);
CREATE INDEX release_events_series_idx
    ON public.release_events (library_id, series_id, created_at DESC);

-- Compact recipient index used by the fanout worker. profile_id has no FK:
-- profiles may live in per-user SQLite stores rather than Postgres, so
-- profile deletion cleans these rows up in code instead of via cascade.
CREATE TABLE public.profile_series_interest (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    library_id integer NOT NULL,
    series_id text NOT NULL,
    favorite boolean NOT NULL DEFAULT false,
    watchlist boolean NOT NULL DEFAULT false,
    continue_watching boolean NOT NULL DEFAULT false,
    next_up_candidate boolean NOT NULL DEFAULT false,
    last_completed_episode_key integer,
    next_expected_episode_key integer,
    last_notified_episode_key integer,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT profile_series_interest_pkey PRIMARY KEY (profile_id, library_id, series_id)
);

CREATE INDEX profile_series_interest_series_idx
    ON public.profile_series_interest (library_id, series_id);
-- Hot fanout path: only rows with at least one active interest flag matter.
CREATE INDEX profile_series_interest_active_idx
    ON public.profile_series_interest (library_id, series_id)
    WHERE favorite OR watchlist OR continue_watching OR next_up_candidate;
CREATE INDEX profile_series_interest_profile_idx
    ON public.profile_series_interest (profile_id, updated_at DESC);

-- Durable per-profile inbox rows. release_event_id is nullable: operational
-- types (e.g. webhook.auto_disabled) have no release event, and retention
-- pruning of old release_events must not delete inbox rows.
CREATE TABLE public.notification_deliveries (
    id text PRIMARY KEY,
    release_event_id text REFERENCES public.release_events(id) ON DELETE SET NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    library_id integer,
    series_id text,
    episode_id text,
    type text NOT NULL,
    reason_flags jsonb NOT NULL,
    status text NOT NULL DEFAULT 'delivered',
    read_at timestamptz,
    delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_deliveries_episode_fields_check CHECK (
        type <> 'episode.available'
        OR (release_event_id IS NOT NULL AND library_id IS NOT NULL
            AND series_id IS NOT NULL AND episode_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX notification_deliveries_profile_event_key
    ON public.notification_deliveries (profile_id, release_event_id)
    WHERE release_event_id IS NOT NULL;
-- Cross-library dedupe: the same episode landing in two libraries (e.g.
-- "TV" and "TV 4K") shares one episode_id; the first release event processed
-- wins and later inserts no-op.
CREATE UNIQUE INDEX notification_deliveries_profile_episode_key
    ON public.notification_deliveries (profile_id, episode_id)
    WHERE type = 'episode.available';

CREATE INDEX notification_deliveries_inbox_idx
    ON public.notification_deliveries (profile_id, created_at DESC);
CREATE INDEX notification_deliveries_unread_idx
    ON public.notification_deliveries (profile_id, read_at, created_at DESC);
CREATE INDEX notification_deliveries_status_idx
    ON public.notification_deliveries (status, created_at);
-- Forward-sync cursor support.
CREATE INDEX notification_deliveries_sync_idx
    ON public.notification_deliveries (created_at, id);

CREATE TABLE public.notification_preferences (
    profile_id text PRIMARY KEY,
    enabled boolean NOT NULL DEFAULT true,
    notify_favorites boolean NOT NULL DEFAULT true,
    notify_watchlist boolean NOT NULL DEFAULT true,
    notify_continue_watching boolean NOT NULL DEFAULT true,
    notify_next_up boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Checkpoint state for the interest backfill / availability seeding tasks.
CREATE TABLE public.notification_backfill_state (
    task text PRIMARY KEY,
    last_processed_key text,
    started_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.notification_backfill_state;
DROP TABLE IF EXISTS public.notification_preferences;
DROP TABLE IF EXISTS public.notification_deliveries;
DROP TABLE IF EXISTS public.profile_series_interest;
DROP TABLE IF EXISTS public.release_events;
DROP TABLE IF EXISTS public.notification_library_seed_state;
DROP TABLE IF EXISTS public.episode_availability;
-- +goose StatementEnd
