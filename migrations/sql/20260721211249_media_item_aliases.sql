-- +goose Up
-- +goose StatementBegin
CREATE TABLE media_item_aliases (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    content_id text NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    title text NOT NULL CHECK (btrim(title) <> ''),
    normalized_title text GENERATED ALWAYS AS (public.normalize_search_text(title)) STORED,
    language text NOT NULL DEFAULT '',
    kind text NOT NULL CHECK (kind IN ('original', 'localized', 'alternate')),
    provider text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_media_item_aliases_unique
    ON media_item_aliases (content_id, normalized_title, language, kind, provider);
CREATE INDEX idx_media_item_aliases_content_provider
    ON media_item_aliases (content_id, provider);
CREATE INDEX idx_media_item_aliases_normalized_trgm
    ON media_item_aliases USING gin (normalized_title public.gin_trgm_ops);
CREATE INDEX idx_media_item_aliases_search_vector
    ON media_item_aliases USING gin (to_tsvector('simple', normalized_title));

-- Persist the batch cursor so an interrupted backfill resumes at its last
-- committed item instead of rescanning the whole catalog on every run.
CREATE TABLE media_item_alias_backfill_state (
    task_key text PRIMARY KEY,
    last_content_id text NOT NULL DEFAULT '',
    completed boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS media_item_alias_backfill_state;
DROP TABLE IF EXISTS media_item_aliases;
