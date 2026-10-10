-- +goose Up
-- +goose StatementBegin
-- The approved community catalog is off until an admin turns it on
-- (1.0 plugin-management AC3). 20260709191109 turned it on for every server
-- that had run before, and an admin's later choice stores the same value, so
-- the two cannot be told apart. Turn it off wherever no installed plugin comes
-- from it; a server already running a community plugin keeps it on so that
-- plugin keeps receiving updates.
UPDATE public.server_settings
SET value = 'false'
WHERE key = 'plugins.include_approved_community_plugins'
  AND LOWER(TRIM(value)) = 'true'
  AND NOT EXISTS (
      SELECT 1
      FROM public.plugin_installations i
      JOIN public.plugin_repositories r ON r.id = i.repository_id
      WHERE r.managed_key = 'approved-community'
  );

UPDATE public.plugin_repositories
SET enabled = false,
    updated_at = NOW()
WHERE managed_key = 'approved-community'
  AND enabled
  AND EXISTS (
      SELECT 1
      FROM public.server_settings
      WHERE key = 'plugins.include_approved_community_plugins'
        AND LOWER(TRIM(value)) = 'false'
  );
-- +goose StatementEnd

-- +goose Down
-- Irreversible: which servers this turned off is not recorded, and turning
-- the catalog back on is an admin choice.
SELECT 1;
