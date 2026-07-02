-- +goose Up
INSERT INTO server_settings (key, value) VALUES
    ('catalog.search.provider', 'postgres'),
    ('catalog.search.meilisearch.url', ''),
    ('catalog.search.meilisearch.api_key', ''),
    ('catalog.search.meilisearch.index', 'silo_media_items'),
    ('catalog.search.meilisearch.timeout_ms', '800'),
    ('catalog.search.meilisearch.matching_strategy', 'last')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE catalog_search_index_events (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL DEFAULT 'meilisearch',
    action TEXT NOT NULL CHECK (action IN ('upsert', 'delete', 'rename')),
    content_id TEXT NOT NULL,
    previous_content_id TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_catalog_search_index_events_pending
    ON catalog_search_index_events (provider, processed_at, available_at, id);

CREATE INDEX idx_catalog_search_index_events_content_id
    ON catalog_search_index_events (content_id);

CREATE TABLE catalog_search_index_state (
    provider TEXT PRIMARY KEY,
    active_index_uid TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL DEFAULT 0,
    document_count INTEGER NOT NULL DEFAULT 0,
    last_rebuild_at TIMESTAMPTZ,
    last_sync_at TIMESTAMPTZ,
    last_processed_event_id BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS catalog_search_index_state;
DROP TABLE IF EXISTS catalog_search_index_events;

DELETE FROM server_settings
WHERE key IN (
    'catalog.search.provider',
    'catalog.search.meilisearch.url',
    'catalog.search.meilisearch.api_key',
    'catalog.search.meilisearch.index',
    'catalog.search.meilisearch.timeout_ms',
    'catalog.search.meilisearch.matching_strategy'
);
