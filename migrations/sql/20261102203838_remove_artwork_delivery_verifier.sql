-- +goose NO TRANSACTION
-- +goose Up
-- The artwork delivery verifier is gone. Silo is the only writer to the
-- assets store and deletes a revision only after retiring it, so catalog
-- reads trust publication manifests on every backend, as local storage and
-- presigned S3 always have. Drop the verifier's verdict state and its saved
-- schedule. Its execution history stays; task history retention prunes it.
DROP INDEX CONCURRENTLY IF EXISTS artwork_delivery_pending_idx;
DROP INDEX CONCURRENTLY IF EXISTS artwork_delivery_due_idx;
ALTER TABLE artwork_revision_gc_candidates
    DROP COLUMN IF EXISTS delivery_keys,
    DROP COLUMN IF EXISTS delivery_scope,
    DROP COLUMN IF EXISTS delivery_checked_at,
    DROP COLUMN IF EXISTS delivery_next_check,
    DROP COLUMN IF EXISTS delivery_lease,
    DROP COLUMN IF EXISTS delivery_failures;
DELETE FROM task_triggers WHERE task_key = 'verify_artwork_delivery';
DELETE FROM task_schedules WHERE task_key = 'verify_artwork_delivery';

-- +goose Down
-- Every revision returns unverified, as it was before its first check.
ALTER TABLE artwork_revision_gc_candidates
    ADD COLUMN IF NOT EXISTS delivery_keys text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS delivery_scope text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS delivery_checked_at timestamptz,
    ADD COLUMN IF NOT EXISTS delivery_next_check timestamptz NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS delivery_lease text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS delivery_failures integer NOT NULL DEFAULT 0;
-- A failed concurrent build leaves an invalid index, so drop before building.
DROP INDEX CONCURRENTLY IF EXISTS artwork_delivery_due_idx;
CREATE INDEX CONCURRENTLY artwork_delivery_due_idx
ON artwork_revision_gc_candidates (delivery_next_check, id)
WHERE deleted_at IS NULL AND cardinality(coalesce(published_keys, object_keys)) > 0;
DROP INDEX CONCURRENTLY IF EXISTS artwork_delivery_pending_idx;
CREATE INDEX CONCURRENTLY artwork_delivery_pending_idx
ON artwork_revision_gc_candidates (delivery_next_check, id)
WHERE deleted_at IS NULL AND cardinality(coalesce(published_keys, object_keys)) > 0
  AND delivery_checked_at IS NULL;
