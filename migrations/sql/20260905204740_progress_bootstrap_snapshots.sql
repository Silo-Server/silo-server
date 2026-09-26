-- +goose Up
CREATE TABLE user_progress_sync_state (
    user_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    generation uuid NOT NULL,
    admission_revision bigint NOT NULL DEFAULT 0
);
CREATE TABLE progress_bootstrap_snapshots (
    id uuid PRIMARY KEY,
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    request_id uuid NOT NULL,
    installation_id text NOT NULL,
    generation uuid NOT NULL,
    access_digest text NOT NULL,
    page_size integer NOT NULL CHECK (page_size BETWEEN 1 AND 200),
    captured_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    item_count integer NOT NULL DEFAULT 0,
    byte_count bigint NOT NULL DEFAULT 0,
    UNIQUE(user_id,profile_id,request_id)
);
CREATE INDEX progress_bootstrap_expiry_idx ON progress_bootstrap_snapshots(expires_at);
CREATE TABLE progress_bootstrap_items (
    snapshot_id uuid NOT NULL REFERENCES progress_bootstrap_snapshots(id) ON DELETE CASCADE,
    ordinal integer NOT NULL,
    payload jsonb NOT NULL,
    PRIMARY KEY(snapshot_id,ordinal)
);

-- +goose Down
DROP TABLE progress_bootstrap_items;
DROP TABLE progress_bootstrap_snapshots;
DROP TABLE user_progress_sync_state;
