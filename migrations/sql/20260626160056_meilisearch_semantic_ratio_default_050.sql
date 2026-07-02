-- +goose Up
UPDATE server_settings
SET value = '0.50'
WHERE key = 'catalog.search.meilisearch.semantic_ratio'
  AND value = '0.30';

INSERT INTO server_settings (key, value)
VALUES ('catalog.search.meilisearch.semantic_ratio', '0.50')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
UPDATE server_settings
SET value = '0.30'
WHERE key = 'catalog.search.meilisearch.semantic_ratio'
  AND value = '0.50';
