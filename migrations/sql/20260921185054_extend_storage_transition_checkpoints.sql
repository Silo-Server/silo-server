-- +goose Up
ALTER TABLE storage_transition_checkpoints
    ADD COLUMN listed_size bigint,
    ADD COLUMN listed_etag text,
    ADD COLUMN listed_mod_time timestamptz,
    ADD COLUMN listing_run_id text,
    ADD COLUMN seen_run_id text;

CREATE INDEX storage_transition_checkpoints_unseen_idx
    ON storage_transition_checkpoints (transition_id, scope, seen_run_id);

-- +goose Down
DROP INDEX IF EXISTS storage_transition_checkpoints_unseen_idx;

ALTER TABLE storage_transition_checkpoints
    DROP COLUMN IF EXISTS seen_run_id,
    DROP COLUMN IF EXISTS listing_run_id,
    DROP COLUMN IF EXISTS listed_mod_time,
    DROP COLUMN IF EXISTS listed_etag,
    DROP COLUMN IF EXISTS listed_size;
