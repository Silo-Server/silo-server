-- media_intro_fingerprints becomes the per-file media analysis artifact table.
-- The change is additive so a rolling deploy needs no maintenance window:
-- rows written by older binaries take the intro fingerprint defaults, and
-- their ON CONFLICT (media_file_id, algorithm_version, config_hash) upserts
-- keep working because the primary key is unchanged. Each kind namespaces its
-- config_hash, so kinds never share a key. Renaming the table can wait for a
-- schema-hygiene maintenance window.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_intro_fingerprints
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'intro_fingerprint',
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'complete',
    ADD COLUMN IF NOT EXISTS detail text,
    ADD COLUMN IF NOT EXISTS failure_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error text,
    ADD COLUMN IF NOT EXISTS retry_after timestamp with time zone,
    ADD COLUMN IF NOT EXISTS recorded_by text NOT NULL DEFAULT '';

ALTER TABLE media_intro_fingerprints
    DROP CONSTRAINT IF EXISTS media_intro_fingerprints_status_check;
-- NOT VALID skips a full-table scan under the ACCESS EXCLUSIVE lock; every
-- existing row took the 'complete' default, so it already satisfies the check.
ALTER TABLE media_intro_fingerprints
    ADD CONSTRAINT media_intro_fingerprints_status_check
    CHECK (status IN ('complete', 'unusable', 'failed')) NOT VALID;

COMMENT ON TABLE media_intro_fingerprints IS
    'Per-file media analysis artifacts by kind; the name predates generalization.';

-- Nothing looks artifacts up by file hash; every read is a primary-key probe.
DROP INDEX IF EXISTS idx_media_intro_fingerprints_hash;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The previous schema holds only complete intro fingerprints, and older
-- binaries read every row as one.
DELETE FROM media_intro_fingerprints
WHERE kind <> 'intro_fingerprint'
   OR status <> 'complete';

ALTER TABLE media_intro_fingerprints
    DROP CONSTRAINT IF EXISTS media_intro_fingerprints_status_check;

ALTER TABLE media_intro_fingerprints
    DROP COLUMN IF EXISTS recorded_by,
    DROP COLUMN IF EXISTS retry_after,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS failure_count,
    DROP COLUMN IF EXISTS detail,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS kind;

COMMENT ON TABLE media_intro_fingerprints IS NULL;

CREATE INDEX IF NOT EXISTS idx_media_intro_fingerprints_hash
    ON media_intro_fingerprints (file_hash, algorithm_version, config_hash);
-- +goose StatementEnd
