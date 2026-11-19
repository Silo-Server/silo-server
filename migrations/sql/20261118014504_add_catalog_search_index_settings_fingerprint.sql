-- +goose Up
-- The index settings the active Meilisearch index was last brought to, as
-- catalogSearchMeilisearchSettingsFingerprint. NULL means unknown: the sync
-- task applies the current settings in place and records them, so an index
-- built before this column existed is updated without a rebuild.
ALTER TABLE catalog_search_index_state ADD COLUMN settings_fingerprint TEXT;

-- +goose Down
ALTER TABLE catalog_search_index_state DROP COLUMN settings_fingerprint;
