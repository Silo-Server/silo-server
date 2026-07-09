-- +goose Up
-- +goose StatementBegin
-- Binary quantization defaults ON for any install with no active catalog
-- search index — fresh installs, and also deployments that configured
-- Meilisearch but never built an index. Deployments with an active index are
-- left unset (= off): flipping quantization changes the index schema-version
-- identity, which closes the incremental-sync gate until a full rebuild runs
-- — that must never happen implicitly on upgrade. Installs with no index yet
-- have no gate to close, so their first rebuild simply starts quantized.
INSERT INTO server_settings (key, value)
SELECT 'catalog.search.meilisearch.binary_quantized', 'true'
WHERE NOT EXISTS (
        SELECT 1 FROM server_settings
        WHERE key = 'catalog.search.meilisearch.binary_quantized')
  AND NOT EXISTS (
        SELECT 1 FROM catalog_search_index_state
        WHERE active_index_uid IS NOT NULL AND active_index_uid <> '');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Intentionally a no-op: deleting the setting could silently change the
-- schema-version identity of an index that was built with quantization.
SELECT 1;
-- +goose StatementEnd
