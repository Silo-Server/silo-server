-- +goose Up
CREATE TABLE worker_http_keys (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    private_key TEXT NOT NULL
);
CREATE TABLE worker_http_nodes (
    node_id INTEGER PRIMARY KEY REFERENCES stream_nodes(id) ON DELETE CASCADE,
    public_key BYTEA NOT NULL CHECK (octet_length(public_key) = 32),
    allow_http BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE worker_http_enrollments (
    ticket_hash BYTEA PRIMARY KEY CHECK (octet_length(ticket_hash) = 32),
    node_id INTEGER NOT NULL REFERENCES stream_nodes(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ
);
CREATE TABLE worker_http_download_attempts (
    node_id INTEGER NOT NULL REFERENCES stream_nodes(id) ON DELETE CASCADE,
    work_id TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    artifact_id TEXT NOT NULL REFERENCES download_artifacts(id) ON DELETE CASCADE,
    lease_owner TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    PRIMARY KEY (node_id, work_id, generation)
);

-- +goose Down
DROP TABLE worker_http_download_attempts;
DROP TABLE worker_http_enrollments;
DROP TABLE worker_http_nodes;
DROP TABLE worker_http_keys;
