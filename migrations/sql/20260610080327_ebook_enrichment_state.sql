-- +goose Up
-- Dedicated failure accounting for the ebook enrichment sweep. Enrichment
-- previously incremented media_items.refresh_failures, which the metadata
-- refresh-debt system also owns (increments on refresh failure, resets on
-- success, reads for debt scoring); the two systems fought over one counter.
CREATE TABLE ebook_enrichment_state (
    content_id text PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE ebook_enrichment_state;
