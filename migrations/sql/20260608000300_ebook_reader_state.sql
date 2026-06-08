-- +goose Up
-- +goose StatementBegin
CREATE TABLE ebook_reader_config (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, content_id)
);

CREATE INDEX ebook_reader_config_profile_updated
    ON ebook_reader_config (user_id, profile_id, updated_at DESC);

CREATE TABLE ebook_reader_annotations (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('highlight', 'note', 'bookmark')),
    cfi_range TEXT,
    location TEXT,
    selected_text TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    style TEXT NOT NULL DEFAULT 'highlight',
    color TEXT NOT NULL DEFAULT '#facc15',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((kind = 'bookmark' AND location IS NOT NULL) OR (kind <> 'bookmark' AND cfi_range IS NOT NULL))
);

CREATE INDEX ebook_reader_annotations_book
    ON ebook_reader_annotations (user_id, profile_id, content_id, updated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ebook_reader_annotations;
DROP TABLE IF EXISTS ebook_reader_config;
-- +goose StatementEnd
