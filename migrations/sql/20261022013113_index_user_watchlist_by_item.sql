-- +goose Up
-- The watchlist arrival notice finds the library watchlist entries for an
-- arrived item (entries promotion moved there before fanout ran). Every other
-- index on the table leads with the account.
CREATE INDEX idx_user_watchlist_item
    ON public.user_watchlist (media_item_id, added_at);

-- +goose Down
DROP INDEX IF EXISTS public.idx_user_watchlist_item;
