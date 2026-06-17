-- +goose Up
-- Dedicated failure accounting for the manga enrichment sweep. Mirrors
-- ebook_enrichment_state: tracks per-item failure counts independently from
-- media_items.refresh_failures so the enrichment sweep and the metadata
-- refresh-debt system do not fight over a shared counter.
CREATE TABLE manga_enrichment_state (
    content_id text PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE manga_enrichment_state;
