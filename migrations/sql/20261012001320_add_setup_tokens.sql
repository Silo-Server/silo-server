-- +goose Up
-- One row per API process that booted while no account existed. Each holds
-- the SHA-256 of the setup token that process printed to its log; any replica
-- accepts any live row, and completing first-run setup deletes them all.
CREATE TABLE public.setup_tokens (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE public.setup_tokens;
