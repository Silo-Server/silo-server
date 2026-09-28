-- +goose Up
-- Storage moves can span hundreds of gigabytes. Keep verified object receipts
-- and page cursors outside server_settings so interrupted jobs can resume
-- without trusting object size or replaying the complete source listing.
CREATE TABLE storage_transition_checkpoints (
    transition_id text NOT NULL,
    scope text NOT NULL,
    object_key text NOT NULL,
    source_size bigint NOT NULL CHECK (source_size >= 0),
    sha256 text NOT NULL CHECK (length(sha256) = 64),
    completed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (transition_id, scope, object_key)
);

CREATE TABLE storage_transition_cursors (
    transition_id text NOT NULL,
    scope text NOT NULL,
    cursor text NOT NULL,
    copied_objects bigint NOT NULL DEFAULT 0 CHECK (copied_objects >= 0),
    copied_bytes bigint NOT NULL DEFAULT 0 CHECK (copied_bytes >= 0),
    completed boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (transition_id, scope)
);

-- +goose Down
DROP TABLE IF EXISTS storage_transition_cursors;
DROP TABLE IF EXISTS storage_transition_checkpoints;
