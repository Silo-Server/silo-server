-- +goose Up
-- +goose StatementBegin

-- Watchlist order mirroring now defaults to enabled: new connections are
-- created with sync_watchlist_order_enabled = true, so flip existing rows to
-- match. The old default was false and the toggle shipped off, so a stored
-- false almost always means "never touched" rather than an explicit opt-out.
-- Providers without the provides_watchlist_order capability ignore the flag
-- at sync time, so enabling it everywhere is harmless.
UPDATE public.watch_provider_connections
SET sync_watchlist_order_enabled = true;

ALTER TABLE public.watch_provider_connections
    ALTER COLUMN sync_watchlist_order_enabled SET DEFAULT true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    ALTER COLUMN sync_watchlist_order_enabled SET DEFAULT false;

-- Down intentionally leaves per-connection values untouched: there is no way
-- to distinguish rows enabled by the Up from rows users enabled themselves.
-- +goose StatementEnd
