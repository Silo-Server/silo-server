-- +goose Up
-- +goose StatementBegin
-- Remove the metadata_providers mirror table.
-- library_provider_chains now references plugin capabilities directly
-- via the composite natural key (plugin_installation_id, capability_id).

BEGIN;

-- Step 1: Add new columns (nullable initially for the data migration).
ALTER TABLE library_provider_chains
    ADD COLUMN plugin_installation_id BIGINT,
    ADD COLUMN capability_id TEXT,
    ADD COLUMN capability_type TEXT NOT NULL DEFAULT 'metadata_provider.v1';

-- Step 2: Populate from metadata_providers settings.
UPDATE library_provider_chains lpc
SET plugin_installation_id = (mp.settings->>'plugin_installation_id')::bigint,
    capability_id = mp.settings->>'capability_id'
FROM metadata_providers mp
WHERE lpc.provider_id = mp.id;

-- Step 3: Delete orphaned rows that couldn't be mapped (provider was
-- uninstalled but chain entry lingered). These are invalid anyway.
DELETE FROM library_provider_chains
WHERE plugin_installation_id IS NULL OR capability_id IS NULL;

-- Step 4: Make new columns NOT NULL now that data is populated.
ALTER TABLE library_provider_chains
    ALTER COLUMN plugin_installation_id SET NOT NULL,
    ALTER COLUMN capability_id SET NOT NULL;

-- Step 5: Drop old constraints and column.
ALTER TABLE library_provider_chains
    DROP CONSTRAINT library_provider_chains_provider_id_fkey;

ALTER TABLE library_provider_chains
    DROP CONSTRAINT library_provider_chains_pkey;

ALTER TABLE library_provider_chains
    DROP COLUMN provider_id;

-- Step 6: New PK and FK.
ALTER TABLE library_provider_chains
    ADD PRIMARY KEY (media_folder_id, plugin_installation_id, capability_id, content_level);

ALTER TABLE library_provider_chains
    ADD CONSTRAINT library_provider_chains_installation_fkey
    FOREIGN KEY (plugin_installation_id)
    REFERENCES plugin_installations(id) ON DELETE CASCADE;

-- Step 7: Drop the mirror table.
DROP TABLE IF EXISTS metadata_providers;
DROP SEQUENCE IF EXISTS metadata_providers_id_seq;

COMMIT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Reverse: recreate metadata_providers and remap chain entries back.
-- Note: display_name may not round-trip perfectly.

BEGIN;

-- Step 1: Recreate the metadata_providers table.
CREATE TABLE metadata_providers (
    id SERIAL PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL DEFAULT 'plugin',
    enabled BOOLEAN NOT NULL DEFAULT true,
    settings JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Step 2: Populate from plugin_capabilities.
INSERT INTO metadata_providers (slug, provider_type, settings)
SELECT
    pc.capability_id,
    'plugin',
    jsonb_build_object(
        'plugin_installation_id', pc.plugin_installation_id::text,
        'capability_id', pc.capability_id,
        'display_name', COALESCE(pc.metadata->>'display_name', pc.capability_id)
    )
FROM plugin_capabilities pc
WHERE pc.capability_type = 'metadata_provider.v1';

-- Step 3: Add provider_id column back to chain entries.
ALTER TABLE library_provider_chains
    ADD COLUMN provider_id INTEGER;

-- Step 4: Map composite key back to provider_id.
UPDATE library_provider_chains lpc
SET provider_id = mp.id
FROM metadata_providers mp
WHERE (mp.settings->>'plugin_installation_id')::bigint = lpc.plugin_installation_id
  AND mp.settings->>'capability_id' = lpc.capability_id;

-- Step 5: Delete any unmappable rows.
DELETE FROM library_provider_chains WHERE provider_id IS NULL;

ALTER TABLE library_provider_chains
    ALTER COLUMN provider_id SET NOT NULL;

-- Step 6: Drop composite key columns and constraints.
ALTER TABLE library_provider_chains
    DROP CONSTRAINT library_provider_chains_installation_fkey;

ALTER TABLE library_provider_chains
    DROP CONSTRAINT library_provider_chains_pkey;

ALTER TABLE library_provider_chains
    DROP COLUMN plugin_installation_id,
    DROP COLUMN capability_id,
    DROP COLUMN capability_type;

-- Step 7: Restore original PK and FK.
ALTER TABLE library_provider_chains
    ADD PRIMARY KEY (media_folder_id, provider_id, content_level);

ALTER TABLE library_provider_chains
    ADD CONSTRAINT library_provider_chains_provider_id_fkey
    FOREIGN KEY (provider_id) REFERENCES metadata_providers(id) ON DELETE CASCADE;

COMMIT;
-- +goose StatementEnd
