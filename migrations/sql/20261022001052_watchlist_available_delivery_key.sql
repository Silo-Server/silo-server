-- +goose Up
-- watchlist.available tells a profile once that a title it watchlisted before
-- the library had it arrived. series_id names the arrived catalog item; the
-- fanout insert's bare ON CONFLICT DO NOTHING honours this key.
CREATE UNIQUE INDEX notification_deliveries_account_profile_watchlist_key
    ON public.notification_deliveries (user_id, profile_id, series_id)
    WHERE type = 'watchlist.available';

-- +goose Down
DROP INDEX IF EXISTS public.notification_deliveries_account_profile_watchlist_key;
