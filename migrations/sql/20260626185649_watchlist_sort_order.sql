-- +goose Up
-- +goose StatementBegin

-- Optional mirroring of an external provider's watchlist sort order. sort_index
-- is NULL for locally-added items (which fall back to added_at ordering) and
-- 0..N-1 for items whose order is synced from a provider (currently MDBList).
ALTER TABLE public.user_watchlist
    ADD COLUMN sort_index integer;

-- Supports "ORDER BY sort_index ASC NULLS LAST, added_at DESC".
CREATE INDEX idx_user_watchlist_profile_sort
    ON public.user_watchlist (user_id, profile_id, sort_index ASC NULLS LAST, added_at DESC);

-- Per-connection toggle: mirror the provider's watchlist order into Silo's
-- watchlist. Gated in the UI by the provider's provides_watchlist_order
-- capability.
ALTER TABLE public.watch_provider_connections
    ADD COLUMN sync_watchlist_order_enabled boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS sync_watchlist_order_enabled;

DROP INDEX IF EXISTS idx_user_watchlist_profile_sort;

ALTER TABLE public.user_watchlist
    DROP COLUMN IF EXISTS sort_index;
-- +goose StatementEnd
