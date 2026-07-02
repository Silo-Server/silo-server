-- +goose Up
INSERT INTO server_settings (key, value) VALUES
    ('catalog.search.meilisearch.semantic_enabled', 'false'),
    ('catalog.search.meilisearch.semantic_ratio', '0.30'),
    ('catalog.search.meilisearch.embedder', 'silo_recommendations')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM server_settings
WHERE key IN (
    'catalog.search.meilisearch.semantic_enabled',
    'catalog.search.meilisearch.semantic_ratio',
    'catalog.search.meilisearch.embedder'
);
