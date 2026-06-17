-- +goose Up
-- +goose StatementBegin
CREATE TABLE manga_chapters (
    chapter_content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    series_content_id  TEXT NOT NULL    REFERENCES media_items(content_id) ON DELETE CASCADE,
    chapter_index      NUMERIC,
    volume             TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX manga_chapters_series ON manga_chapters (series_content_id, chapter_index NULLS LAST);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS manga_chapters;
-- +goose StatementEnd
