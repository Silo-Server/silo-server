-- +goose Up
-- A whole-series or whole-season watched marker stays kept after it matches:
-- it covers episodes a library may only gain later. applied_episodes
-- identifies the local episodes it covered when last applied (an MD5 of their
-- content ids), so a later sync applies it again whenever that set changes.
ALTER TABLE public.watch_provider_pending_watches
    ADD COLUMN applied_episodes text NOT NULL DEFAULT '';

-- The watched cursor moves to plugin.remote.watched.v3: a host without
-- finished-series support skipped those rows and moved its cursor past them,
-- so every connection reads its watched history once more. Drop the old key.
UPDATE public.watch_provider_connections
SET sync_cursors = sync_cursors - 'plugin.remote.watched.v2'
WHERE sync_cursors ? 'plugin.remote.watched.v2';

-- +goose Down
ALTER TABLE public.watch_provider_pending_watches
    DROP COLUMN IF EXISTS applied_episodes;
