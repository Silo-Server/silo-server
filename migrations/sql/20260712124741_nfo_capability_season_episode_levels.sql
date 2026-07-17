-- +goose Up
-- Declare season/episode support for the built-in NFO provider (#216,
-- Phase D). The capability's default_priority map enumerates the content
-- levels a provider participates in: without these keys the startup builtin
-- chain sync never appends the NFO provider to season/episode chains, so
-- season.nfo names and episode NFO titles could never resolve. Priority 1
-- matches the movie/series declaration (local curation wins when enabled);
-- the capability stays default_enabled=false so nothing activates on its own.
UPDATE plugin_capabilities pc
SET metadata = jsonb_set(
        pc.metadata,
        '{default_priority}',
        COALESCE(pc.metadata->'default_priority', '{}'::jsonb)
            || '{"season": 1, "episode": 1}'::jsonb
    )
FROM plugin_installations pi
WHERE pc.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND pc.capability_type = 'metadata_provider.v1' AND pc.capability_id = 'nfo';

-- +goose Down
UPDATE plugin_capabilities pc
SET metadata = jsonb_set(
        pc.metadata,
        '{default_priority}',
        (pc.metadata->'default_priority') - 'season' - 'episode'
    )
FROM plugin_installations pi
WHERE pc.plugin_installation_id = pi.id
  AND pi.plugin_id = 'silo.builtin' AND pi.kind = 'builtin'
  AND pc.capability_type = 'metadata_provider.v1' AND pc.capability_id = 'nfo'
  AND pc.metadata ? 'default_priority';
