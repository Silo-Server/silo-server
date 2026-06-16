-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS literary_works (
    work_id TEXT PRIMARY KEY,
    canonical_title TEXT NOT NULL,
    sort_title TEXT,
    normalized_title TEXT NOT NULL,
    primary_author_key TEXT NOT NULL DEFAULT '',
    primary_cover_content_id TEXT REFERENCES media_items(content_id) ON DELETE SET NULL,
    description TEXT,
    published_date DATE,
    publisher TEXT,
    genres TEXT[] NOT NULL DEFAULT '{}',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS literary_work_items (
    work_id TEXT NOT NULL REFERENCES literary_works(work_id) ON DELETE CASCADE,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    format_type TEXT NOT NULL,
    link_source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    confirmed_at TIMESTAMPTZ,
    ignored_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (work_id, content_id),
    UNIQUE (content_id),
    CHECK (format_type IN ('ebook', 'audiobook', 'comic', 'manga')),
    CHECK (link_source IN ('manual', 'external_id', 'metadata_match', 'series_match', 'scan_seed')),
    CHECK (confidence >= 0 AND confidence <= 1)
);

CREATE TABLE IF NOT EXISTS literary_work_match_decisions (
    source_content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    target_content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    decision TEXT NOT NULL,
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_content_id, target_content_id),
    CHECK (source_content_id <> target_content_id),
    CHECK (decision IN ('confirmed', 'ignored'))
);

CREATE INDEX IF NOT EXISTS idx_literary_works_normalized
    ON literary_works (normalized_title, primary_author_key);

CREATE INDEX IF NOT EXISTS idx_literary_work_items_content
    ON literary_work_items (content_id);

CREATE INDEX IF NOT EXISTS idx_literary_work_items_format
    ON literary_work_items (format_type, work_id);

CREATE INDEX IF NOT EXISTS idx_literary_work_match_decisions_target
    ON literary_work_match_decisions (target_content_id, decision);

CREATE OR REPLACE FUNCTION delete_empty_literary_works()
RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM literary_works lw
    WHERE lw.work_id = OLD.work_id
      AND NOT EXISTS (
          SELECT 1 FROM literary_work_items lwi WHERE lwi.work_id = OLD.work_id
      );
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_delete_empty_literary_works ON literary_work_items;
CREATE TRIGGER trg_delete_empty_literary_works
AFTER DELETE ON literary_work_items
FOR EACH ROW EXECUTE FUNCTION delete_empty_literary_works();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_delete_empty_literary_works ON literary_work_items;
DROP FUNCTION IF EXISTS delete_empty_literary_works();
DROP TABLE IF EXISTS literary_work_match_decisions;
DROP TABLE IF EXISTS literary_work_items;
DROP TABLE IF EXISTS literary_works;
-- +goose StatementEnd
