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
--
-- Every chain row's order and on/off state is kept before and after the
-- change, so the Down can put back what admins had, including libraries that
-- had NFO on, without undoing edits made after the upgrade.
CREATE TABLE local_metadata_default_on_chain_backup (
    media_folder_id integer NOT NULL,
    plugin_installation_id bigint NOT NULL,
    capability_id text NOT NULL,
    content_level text NOT NULL,
    priority integer NOT NULL,
    enabled boolean NOT NULL,
    up_priority integer,
    up_enabled boolean,
    PRIMARY KEY (media_folder_id, plugin_installation_id, capability_id, content_level)
);
INSERT INTO local_metadata_default_on_chain_backup
    (media_folder_id, plugin_installation_id, capability_id, content_level, priority, enabled)
SELECT media_folder_id, plugin_installation_id, capability_id, content_level, priority, enabled
FROM library_provider_chains;

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

UPDATE local_metadata_default_on_chain_backup b
SET up_priority = c.priority, up_enabled = c.enabled
FROM library_provider_chains c
WHERE c.media_folder_id = b.media_folder_id
  AND c.plugin_installation_id = b.plugin_installation_id
  AND c.capability_id = b.capability_id
  AND c.content_level = b.content_level;

-- +goose Down
-- Restores the opt-in default and puts each chain row's priority and on/off
-- state back to what it was before the upgrade, each only while it still has
-- the value the Up gave it, so edits made after the upgrade stay. Builtin NFO
-- rows added since (new libraries, the startup sync) go back to the old
-- default, off. Per-level rows the startup sync copied from a legacy ('')
-- chain after the upgrade take that legacy row's snapshot. Each chain is then
-- renumbered so a restored priority and one an admin set since never tie; the
-- admin's comes first.

-- Give each per-level copy of a legacy row that legacy row's snapshot, so the
-- steps below treat it like any other snapshotted row.
INSERT INTO local_metadata_default_on_chain_backup
    (media_folder_id, plugin_installation_id, capability_id, content_level, priority, enabled, up_priority, up_enabled)
SELECT c.media_folder_id, c.plugin_installation_id, c.capability_id, c.content_level,
       l.priority, l.enabled, l.up_priority, l.up_enabled
FROM library_provider_chains c
JOIN local_metadata_default_on_chain_backup l
  ON l.media_folder_id = c.media_folder_id
 AND l.plugin_installation_id = c.plugin_installation_id
 AND l.capability_id = c.capability_id
 AND l.content_level = ''
WHERE c.content_level <> ''
ON CONFLICT DO NOTHING;

UPDATE plugin_capabilities pc
SET metadata = jsonb_set(pc.metadata, '{default_enabled}', 'false'::jsonb)
FROM plugin_installations pi
WHERE pc.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND pc.capability_type = 'metadata_provider.v1' AND pc.capability_id = 'nfo';

UPDATE library_provider_chains c
SET priority = CASE WHEN c.priority = b.up_priority THEN b.priority ELSE c.priority END,
    enabled = CASE WHEN c.enabled = b.up_enabled THEN b.enabled ELSE c.enabled END
FROM local_metadata_default_on_chain_backup b
WHERE c.media_folder_id = b.media_folder_id
  AND c.plugin_installation_id = b.plugin_installation_id
  AND c.capability_id = b.capability_id
  AND c.content_level = b.content_level
  AND (c.priority = b.up_priority OR c.enabled = b.up_enabled);

UPDATE library_provider_chains c
SET enabled = false
FROM plugin_installations pi
WHERE c.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND c.capability_id = 'nfo'
  AND c.enabled
  AND NOT EXISTS (
      SELECT 1 FROM local_metadata_default_on_chain_backup b
      WHERE b.media_folder_id = c.media_folder_id
        AND b.plugin_installation_id = c.plugin_installation_id
        AND b.capability_id = c.capability_id
        AND b.content_level = c.content_level
  );

UPDATE library_provider_chains c
SET priority = ranked.position
FROM (
    SELECT c2.media_folder_id, c2.plugin_installation_id, c2.capability_id, c2.content_level,
           ROW_NUMBER() OVER (
               PARTITION BY c2.media_folder_id, c2.content_level
               ORDER BY c2.priority,
                        COALESCE(c2.priority = b.priority AND b.priority <> b.up_priority, false),
                        c2.plugin_installation_id, c2.capability_id
           ) - 1 AS position
    FROM library_provider_chains c2
    LEFT JOIN local_metadata_default_on_chain_backup b
      ON b.media_folder_id = c2.media_folder_id
     AND b.plugin_installation_id = c2.plugin_installation_id
     AND b.capability_id = c2.capability_id
     AND b.content_level = c2.content_level
) ranked
WHERE c.media_folder_id = ranked.media_folder_id
  AND c.plugin_installation_id = ranked.plugin_installation_id
  AND c.capability_id = ranked.capability_id
  AND c.content_level = ranked.content_level
  AND c.priority <> ranked.position;

DROP TABLE local_metadata_default_on_chain_backup;
