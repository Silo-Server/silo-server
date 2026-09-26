-- +goose Up
-- +goose StatementBegin
-- Sonyflake machine IDs leased by Silo processes. Every process that generates
-- IDs (internal/idgen) claims one row at startup and renews it while running,
-- so no two processes sharing this database use the same machine ID at once.
-- Rows are never deleted: a claim takes a machine ID that has never been used
-- before it reuses one, and reuses only the longest-expired lease.
--
-- token identifies the process holding the lease; holder is the node name,
-- for operators only.
CREATE TABLE idgen_machine_leases (
    machine_id INTEGER PRIMARY KEY CHECK (machine_id BETWEEN 0 AND 65535),
    token UUID NOT NULL,
    holder TEXT NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS idgen_machine_leases;
-- +goose StatementEnd
