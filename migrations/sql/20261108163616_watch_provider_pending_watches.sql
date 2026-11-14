-- +goose Up
-- A provider watch of a title no library holds yet. Watched imports read the
-- provider incrementally, so such a watch is never returned again; it is kept
-- here and matched again on later syncs. Rows belong to the provider account
-- that returned them.
CREATE TABLE public.watch_provider_pending_watches (
    connection_id uuid NOT NULL REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    provider_account_id text NOT NULL,
    kind text NOT NULL,
    provider_item_key text NOT NULL,
    last_watched_at timestamptz NOT NULL,
    watch jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, kind, provider_item_key)
);

-- The watched cursor moves to plugin.remote.watched.v2, so every connection
-- reads its watched history once more in full and keeps what it skipped
-- before. The old key is ignored; drop it.
UPDATE public.watch_provider_connections
SET sync_cursors = sync_cursors - 'plugin.remote.watched'
WHERE sync_cursors ? 'plugin.remote.watched';

-- +goose Down
DROP TABLE IF EXISTS public.watch_provider_pending_watches;
