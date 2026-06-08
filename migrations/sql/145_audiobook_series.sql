-- +goose Up
-- +goose StatementBegin
-- Audiobook series membership. The scanner extracts series_name/sequence
-- from tag fields (series / mvnm / series-part / mvin), but until this
-- migration there was nowhere to persist them. Detail page surfaces this
-- as the "In this series" rail.
CREATE TABLE audiobook_series (
    content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    series_name TEXT NOT NULL,
    series_index NUMERIC,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX audiobook_series_name_lower
    ON audiobook_series (LOWER(series_name), series_index NULLS LAST);

-- Best-effort backfill from titles that follow the common "Series N - Book"
-- pattern (e.g. "A DI Amy Winter Thriller 5 - In Cold Blood"). Books that
-- don't match are simply omitted; a future scan can write any tag-derived
-- values authoritatively, overwriting via the upsert in the scanner.
INSERT INTO audiobook_series (content_id, series_name, series_index)
SELECT
    mi.content_id,
    TRIM(BOTH ' ' FROM substring(mi.title FROM '^(.+[^\s-])\s+\d+(?:\.\d+)?\s*-\s*.+$')) AS series_name,
    substring(mi.title FROM '^.+[^\s-]\s+(\d+(?:\.\d+)?)\s*-\s*.+$')::NUMERIC AS series_index
FROM media_items mi
WHERE mi.type = 'audiobook'
  AND mi.title ~ '^.+[^\s-]\s+\d+(?:\.\d+)?\s*-\s*.+$'
ON CONFLICT (content_id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audiobook_series;
-- +goose StatementEnd
