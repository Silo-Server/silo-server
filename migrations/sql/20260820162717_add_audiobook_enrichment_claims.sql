-- +goose Up
-- The audiobook sweep runs in every server process. A durable lease prevents
-- replicas from selecting the same oldest batch and issuing duplicate provider
-- calls. IF NOT EXISTS keeps this safe for installations that briefly ran the
-- pre-merge enrichment branch before this follow-up migration existed.
ALTER TABLE audiobook_enrichment_state
    ADD COLUMN IF NOT EXISTS claim_token text,
    ADD COLUMN IF NOT EXISTS lease_until timestamptz;

-- +goose Down
ALTER TABLE audiobook_enrichment_state
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS claim_token;
