-- Silo no longer supports Trakt watch sync.
--
--   * Trakt connections are deleted with their per-connection state (sync
--     runs, export records, scrobble sessions, favorites, ratings, dropped
--     shows), as a disconnect would. Watch history and other profile state
--     imported from Trakt stays.
--   * The legacy Trakt app credentials and the plugin migration markers are
--     deleted, and an installed Trakt watch-sync plugin is disabled. The
--     server refuses to register it, so re-enabling it does nothing; an admin
--     uninstalls it to remove its files and settings.

-- +goose Up
DELETE FROM public.watch_provider_auth_sessions WHERE provider = 'trakt';
DELETE FROM public.watch_provider_connections WHERE provider = 'trakt';

DELETE FROM public.server_settings
 WHERE key IN (
     'watchsync.trakt.client_id',
     'watchsync.trakt.client_secret',
     'watchsync.plugin_migration.trakt',
     'watchsync.plugin_app_seeded.trakt'
 );

UPDATE public.plugin_installations
   SET enabled = false,
       updated_at = now()
 WHERE plugin_id = 'silo.watchprovider.trakt'
   AND enabled;

-- +goose Down
-- Irreversible: the connections, tokens and app credentials are not recorded
-- anywhere. An older server auto-installs the Trakt plugin only for Trakt
-- connections, which no longer exist.
SELECT 1;
