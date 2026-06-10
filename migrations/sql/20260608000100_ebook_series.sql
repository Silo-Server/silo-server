-- +goose Up
-- +goose StatementBegin
CREATE TABLE ebook_series (
    content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    series_name TEXT NOT NULL,
    series_index NUMERIC,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ebook_series_name_lower
    ON ebook_series (LOWER(series_name), series_index NULLS LAST);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ebook_series;
-- +goose StatementEnd
