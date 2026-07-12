-- +goose Up
-- Register built-in host metadata providers as data (#216, Phase A).
--
-- Chain rows require a real plugin_installations id (FK + composite PK), so
-- built-ins are represented by one reserved installation row of kind
-- 'builtin' plus ordinary plugin_capabilities rows. The install_path is a
-- guaranteed-nonexistent sentinel: InstallationStore.Delete removes the
-- install directory on disk, so the sentinel must never resolve to a real
-- directory (the store additionally rejects deleting builtin rows).
--
-- Statement order matters: the partial unique index must exist before any
-- ON CONFLICT targets it, and the installation insert uses
-- INSERT ... SELECT ... WHERE NOT EXISTS because plugin_id had no unique
-- constraint before this migration.
ALTER TABLE plugin_installations
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'plugin'
    CONSTRAINT plugin_installations_kind_check CHECK (kind IN ('plugin', 'builtin'));

CREATE UNIQUE INDEX idx_plugin_installations_builtin_plugin_id
    ON plugin_installations (plugin_id)
    WHERE kind = 'builtin';

INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
SELECT 'silo.builtin', '0', '/nonexistent/silo-builtin', true, 'manual', 'builtin'
WHERE NOT EXISTS (
    SELECT 1 FROM plugin_installations WHERE plugin_id = 'silo.builtin'
);

-- The NFO provider: seeded disabled everywhere (default_enabled=false), at
-- declared priority 1 for movie/series when an admin enables it.
INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
SELECT pi.id, 'metadata_provider.v1', 'nfo',
       '{"display_name":"NFO Files","default_priority":{"movie":1,"series":1},"default_enabled":false}'::jsonb
FROM plugin_installations pi
WHERE pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
ON CONFLICT DO NOTHING;

-- +goose Down
-- Rolling back deletes the reserved installation row; library_provider_chains
-- rows referencing it cascade, so any admin-configured NFO chain placement is
-- lost on rollback (accepted and documented).
DELETE FROM plugin_capabilities pc
USING plugin_installations pi
WHERE pc.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND pc.capability_type = 'metadata_provider.v1' AND pc.capability_id = 'nfo';

DELETE FROM plugin_installations WHERE plugin_id = 'silo.builtin' AND kind = 'builtin';

DROP INDEX IF EXISTS idx_plugin_installations_builtin_plugin_id;

ALTER TABLE plugin_installations DROP COLUMN kind;
