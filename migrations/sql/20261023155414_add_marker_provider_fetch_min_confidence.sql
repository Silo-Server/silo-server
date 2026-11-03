-- +goose Up
-- Markers a provider rates below this confidence are left out of lookups, so
-- another provider or local detection can fill the kind. 0 keeps every marker,
-- as lookups did before the column existed.
ALTER TABLE marker_provider_config
    ADD COLUMN IF NOT EXISTS fetch_min_confidence double precision NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE marker_provider_config
    DROP COLUMN IF EXISTS fetch_min_confidence;
