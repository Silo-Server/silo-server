-- +goose Up
INSERT INTO server_settings (key, value) VALUES
    ('catalog.search.meilisearch.sync_batch_size', '500'),
    ('catalog.search.meilisearch.rebuild_batch_size', '5000'),
    ('catalog.search.meilisearch.rebuild_task_queue_depth', '4'),
    ('catalog.search.meilisearch.index_types', '')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM server_settings
WHERE key IN (
    'catalog.search.meilisearch.sync_batch_size',
    'catalog.search.meilisearch.rebuild_batch_size',
    'catalog.search.meilisearch.rebuild_task_queue_depth',
    'catalog.search.meilisearch.index_types'
);
