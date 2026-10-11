-- +goose Up
-- Local metadata (the built-in NFO provider: .nfo text and sidecar artwork) is
-- on by default and always runs ahead of remote providers.
--
-- 1. New libraries seed it enabled, and the chain-less fallback includes it.
-- 2. Every existing chain row for it is enabled, at every content level,
--    including libraries where the startup sync appended it disabled and last.
-- 3. Built-in rows move to the front of each (library, content level) chain;
--    the other providers keep their relative order. Chain resolution pins the
--    built-in tier first anyway; this makes the stored order agree with it.
--
-- Without .nfo files or sidecar art the provider finds nothing and changes
-- nothing.
UPDATE plugin_capabilities pc
SET metadata = jsonb_set(pc.metadata, '{default_enabled}', 'true'::jsonb)
FROM plugin_installations pi
WHERE pc.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND pc.capability_type = 'metadata_provider.v1' AND pc.capability_id = 'nfo';

UPDATE library_provider_chains c
SET enabled = true
FROM plugin_installations pi
WHERE c.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND c.capability_id = 'nfo'
  AND NOT c.enabled;

UPDATE library_provider_chains c
SET priority = ranked.position
FROM (
    SELECT c2.media_folder_id, c2.plugin_installation_id, c2.capability_id, c2.content_level,
           ROW_NUMBER() OVER (
               PARTITION BY c2.media_folder_id, c2.content_level
               ORDER BY (pi.kind = 'builtin') DESC, c2.priority, c2.plugin_installation_id, c2.capability_id
           ) - 1 AS position
    FROM library_provider_chains c2
    JOIN plugin_installations pi ON pi.id = c2.plugin_installation_id
) ranked
WHERE c.media_folder_id = ranked.media_folder_id
  AND c.plugin_installation_id = ranked.plugin_installation_id
  AND c.capability_id = ranked.capability_id
  AND c.content_level = ranked.content_level
  AND c.priority <> ranked.position;

-- +goose Down
-- Restores the opt-in default and turns the built-in rows off again. Which
-- libraries had an admin enable NFO before this migration is not recorded, so
-- those lose it too, and the rows stay at the front of their chains, where
-- being disabled leaves them inert.
UPDATE plugin_capabilities pc
SET metadata = jsonb_set(pc.metadata, '{default_enabled}', 'false'::jsonb)
FROM plugin_installations pi
WHERE pc.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND pc.capability_type = 'metadata_provider.v1' AND pc.capability_id = 'nfo';

UPDATE library_provider_chains c
SET enabled = false
FROM plugin_installations pi
WHERE c.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND c.capability_id = 'nfo'
  AND c.enabled;
