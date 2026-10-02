-- +goose Up
CREATE TABLE userdb_import_receipts (
    user_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    installation_id uuid NOT NULL,
    source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    schema_version integer NOT NULL CHECK (schema_version = 22),
    mapping_version integer NOT NULL CHECK (mapping_version = 1),
    progress_generation uuid NOT NULL,
    verification jsonb NOT NULL CHECK (jsonb_typeof(verification) = 'object'),
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
COMMENT ON TABLE userdb_import_receipts IS
    'Atomic account import evidence only; never authorizes a userstore provider switch.';

-- +goose Down
DROP TABLE userdb_import_receipts;
