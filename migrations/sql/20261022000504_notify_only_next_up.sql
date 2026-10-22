-- +goose Up
-- New-episode notifications now come only from Next Up: favorites, the
-- watchlist and Continue Watching no longer notify for episodes, so their
-- interest flags and the profile and webhook switches for them go. Stop every
-- old API replica before migrating: old binaries read the dropped columns.
--
-- Next Up now waits for one exact episode, or with next_expected_open for the
-- first episode at or past next_expected_episode_key when the catalog knew no
-- unstarted episode after the anchor. Existing rows hold the old cursor
-- (last completed + 1, matched at or past), which is the open form, so they
-- keep notifying as before until the interest rebuild recomputes them.
ALTER TABLE public.profile_series_interest
    ADD COLUMN next_expected_open boolean NOT NULL DEFAULT false;
UPDATE public.profile_series_interest
SET next_expected_open = true
WHERE next_expected_episode_key IS NOT NULL;

-- Rows that existed only for a removed reason can never notify.
DELETE FROM public.profile_series_interest
WHERE NOT next_up_candidate
  AND last_completed_episode_key IS NULL
  AND next_expected_episode_key IS NULL;

-- Dropping the columns drops profile_series_interest_active_idx, whose
-- predicate reads them; the fanout path now reads Next Up candidates only.
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

-- The per-series burst cap is gone: one notification per Next Up position
-- already bounds a season pack.
DELETE FROM public.server_settings WHERE key = 'notifications.fanout.max_series_burst';

-- +goose Down
-- The removed switches come back on, their default; the removed interest
-- flags come back unset until the interest rebuild runs on the old build.
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
    ADD COLUMN continue_watching boolean NOT NULL DEFAULT false,
    DROP COLUMN next_expected_open;
CREATE INDEX profile_series_interest_active_idx
    ON public.profile_series_interest (library_id, series_id)
    WHERE favorite OR watchlist OR continue_watching OR next_up_candidate;
