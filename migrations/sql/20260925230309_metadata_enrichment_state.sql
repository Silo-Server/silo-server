-- +goose Up
-- +goose StatementBegin
-- What each bulk-lookup enrichment provider (a metadata plugin that declares
-- lookup_provider_ids and bulk_lookup_limit, such as MDBList) last answered for
-- an item. The bulk enrichment pass selects items with no row here, or whose
-- next_check_at has passed, so it resumes where it stopped after a restart or
-- an exhausted provider quota.
--
-- outcome is 'found' (the provider returned data; never checked again, so
-- next_check_at is NULL), 'empty' (the provider had nothing for the item), or
-- 'failed' (the lookup or the write failed for this item alone). A failure
-- that concerns the provider as a whole, such as a spent quota, records
-- nothing.
CREATE TABLE metadata_enrichment_state (
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE ON UPDATE CASCADE,
    provider TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('found', 'empty', 'failed')),
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_check_at TIMESTAMPTZ,
    PRIMARY KEY (content_id, provider)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS metadata_enrichment_state;
-- +goose StatementEnd
