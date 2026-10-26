-- +goose Up
-- +goose StatementBegin

-- A profile's dropped series. A drop hides the series from Next Up and
-- Continue Watching while it is active: until any episode of the series has
-- progress or history-hide activity newer than dropped_at. Watching the series
-- again therefore undrops it without a write. There is no profile foreign key:
-- shared tables do not reference profile rows, and DeleteProfile removes a
-- profile's rows explicitly.
CREATE TABLE public.user_dropped_series (
    user_id integer NOT NULL
        REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    series_id text NOT NULL,
    dropped_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, series_id)
);

CREATE INDEX idx_user_dropped_series_series
    ON public.user_dropped_series (series_id);

-- Two-way dropped-show sync. On for existing connections that sync anything,
-- so the next sync imports the drops already recorded on the provider. A
-- connection with every sync setting off stays paused. New connections are
-- created with it on by the service.
ALTER TABLE public.watch_provider_connections
    ADD COLUMN sync_dropped_enabled boolean NOT NULL DEFAULT false;

UPDATE public.watch_provider_connections
SET sync_dropped_enabled = true
WHERE import_watched_enabled
   OR import_progress_enabled
   OR export_watched_enabled
   OR export_unwatched_enabled
   OR import_favorites_enabled
   OR export_favorites_enabled
   OR sync_favorite_removals_enabled
   OR import_watchlist_enabled
   OR export_watchlist_enabled
   OR sync_watchlist_removals_enabled
   OR scrobble_enabled
   OR import_ratings_enabled
   OR export_ratings_enabled;

-- The series Silo and the provider last agreed are dropped, per connection. It
-- is the base of the three-way merge that decides which side changed; no row
-- means the two sides agreed the series is not dropped. remote_seen records
-- that a provider read confirmed the drop, so a series missing from a later
-- complete read counts as a remote undrop only when the provider is known to
-- have held it. provider_account_id scopes the row to the account it was
-- agreed with; rows of another account are ignored.
CREATE TABLE public.watch_provider_dropped_items (
    connection_id uuid NOT NULL
        REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    provider_account_id text NOT NULL DEFAULT '',
    series_id text NOT NULL,
    provider_item_key text NOT NULL DEFAULT '',
    remote_seen boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, series_id)
);

CREATE INDEX idx_watch_provider_dropped_items_series
    ON public.watch_provider_dropped_items (series_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.watch_provider_dropped_items;

ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS sync_dropped_enabled;

DROP TABLE IF EXISTS public.user_dropped_series;
-- +goose StatementEnd
