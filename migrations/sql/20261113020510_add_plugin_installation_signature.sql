-- +goose Up
-- The outcome of the release signature check run when this package was
-- installed (docs/architecture/plugin-trust.md#release-signatures). Rows that
-- predate the check stay 'unknown'; the bundle itself is not stored.
ALTER TABLE plugin_installations
    ADD COLUMN signature_state TEXT NOT NULL DEFAULT 'unknown'
        CONSTRAINT plugin_installations_signature_state_check
        CHECK (signature_state IN ('unknown', 'verified', 'unsigned', 'invalid')),
    ADD COLUMN signature_source_repo TEXT,
    ADD COLUMN signature_verified_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE plugin_installations
    DROP COLUMN signature_verified_at,
    DROP COLUMN signature_source_repo,
    DROP COLUMN signature_state;
