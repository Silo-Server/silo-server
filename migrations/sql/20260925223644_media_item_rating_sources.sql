-- +goose Up
-- +goose StatementBegin
-- Per-source ratings a metadata provider reported under ratings.sources
-- (IMDb, Metacritic, Letterboxd, ...), one row per item and source, on a
-- common 0-100 scale. The four rating_* columns on media_items keep their own
-- scales and their sort and filter behavior; this table only adds the sources
-- they cannot hold. The source vocabulary is validated in code rather than by
-- a CHECK, so a new source does not need a migration.
CREATE TABLE media_item_rating_sources (
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE ON UPDATE CASCADE,
    source TEXT NOT NULL,
    score DOUBLE PRECISION NOT NULL CHECK (score >= 0 AND score <= 100),
    -- NULL when the provider did not report a vote count.
    votes BIGINT CHECK (votes >= 0),
    -- Slug of the metadata provider that supplied the row.
    provider TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, source)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS media_item_rating_sources;
-- +goose StatementEnd
