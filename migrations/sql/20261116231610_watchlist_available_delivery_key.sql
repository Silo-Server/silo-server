-- +goose NO TRANSACTION

-- +goose Up
-- watchlist.available tells a profile once that a title it watchlisted before
-- the library had it arrived. series_id names the arrived catalog item; the
-- fanout insert's bare ON CONFLICT DO NOTHING honors this key. No row has the
-- type yet, so the index starts empty, but building it still reads the whole
-- table.
--
-- The index is built concurrently so inbox reads and delivery inserts keep
-- running. The migration only runs when it is not yet recorded, so an index
-- found here is the possibly invalid leftover of an interrupted build.
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_account_profile_watchlist_key;

CREATE UNIQUE INDEX CONCURRENTLY notification_deliveries_account_profile_watchlist_key
    ON public.notification_deliveries (user_id, profile_id, series_id)
    WHERE type = 'watchlist.available';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.notification_deliveries_account_profile_watchlist_key;
