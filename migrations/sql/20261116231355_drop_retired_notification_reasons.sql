-- +goose Up
-- New-episode notifications now come only from Next Up. Favorites, the
-- watchlist and Continue Watching no longer notify for episodes, so their
-- interest flags and the profile and webhook switches for them go.
--
-- Stop every old API replica before migrating: older binaries read and write
-- the dropped columns (docs/architecture/notifications.md, "Upgrade").

-- Rows kept only by a removed reason can never notify.
DELETE FROM public.profile_series_interest
WHERE NOT next_up_candidate
  AND last_completed_episode_key IS NULL;

-- The old rules notified every arriving episode at or past the cursor, so a
-- row's last notified episode can lie beyond the episode the profile waits
-- for: the catalog lists an episode at or after the cursor and below the
-- mark. Under the new rule that mark would silence the awaited episode when
-- it arrives after a later one. A mark on the first listed episode at or
-- after the cursor stays: it says the profile was told about that episode.
-- The bounds keep the key expression inside int4, as ValidEpisodeOrdinals
-- does (internal/notifications/episode_key.go).
UPDATE public.profile_series_interest psi
SET last_notified_episode_key = NULL
WHERE psi.last_notified_episode_key > psi.next_expected_episode_key
  AND EXISTS (
    SELECT 1 FROM public.episodes e
    WHERE e.series_id = psi.series_id
      AND e.season_number BETWEEN 0 AND 2146
      AND e.episode_number BETWEEN 0 AND 999999
      AND e.season_number * 1000000 + e.episode_number >= psi.next_expected_episode_key
      AND e.season_number * 1000000 + e.episode_number < psi.last_notified_episode_key);

-- Dropping the columns drops profile_series_interest_active_idx, whose
-- predicate reads them. Fanout now reads Next Up candidates only.
ALTER TABLE public.profile_series_interest
    DROP COLUMN favorite,
    DROP COLUMN watchlist,
    DROP COLUMN continue_watching;
CREATE INDEX profile_series_interest_active_idx
    ON public.profile_series_interest (library_id, series_id)
    WHERE next_up_candidate;

ALTER TABLE public.notification_preferences
    DROP COLUMN notify_favorites,
    DROP COLUMN notify_watchlist,
    DROP COLUMN notify_continue_watching;

ALTER TABLE public.notification_webhooks
    DROP COLUMN notify_favorites,
    DROP COLUMN notify_watchlist,
    DROP COLUMN notify_continue_watching;

-- The per-series burst cap is gone: the Next Up rule lets one episode of a
-- series notify a profile, which already bounds a season pack.
DELETE FROM public.server_settings WHERE key = 'notifications.fanout.max_series_burst';

-- +goose Down
-- The removed switches come back on, their default. The removed interest
-- flags come back unset until the interest rebuild runs on the old build, and
-- the deleted rows, the cleared last notified marks and the burst setting do
-- not come back.
ALTER TABLE public.notification_webhooks
    ADD COLUMN notify_favorites boolean NOT NULL DEFAULT true,
    ADD COLUMN notify_watchlist boolean NOT NULL DEFAULT true,
    ADD COLUMN notify_continue_watching boolean NOT NULL DEFAULT true;

ALTER TABLE public.notification_preferences
    ADD COLUMN notify_favorites boolean NOT NULL DEFAULT true,
    ADD COLUMN notify_watchlist boolean NOT NULL DEFAULT true,
    ADD COLUMN notify_continue_watching boolean NOT NULL DEFAULT true;

DROP INDEX public.profile_series_interest_active_idx;
ALTER TABLE public.profile_series_interest
    ADD COLUMN favorite boolean NOT NULL DEFAULT false,
    ADD COLUMN watchlist boolean NOT NULL DEFAULT false,
    ADD COLUMN continue_watching boolean NOT NULL DEFAULT false;
CREATE INDEX profile_series_interest_active_idx
    ON public.profile_series_interest (library_id, series_id)
    WHERE favorite OR watchlist OR continue_watching OR next_up_candidate;
