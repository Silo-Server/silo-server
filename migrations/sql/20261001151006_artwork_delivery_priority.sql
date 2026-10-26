-- +goose NO TRANSACTION
-- +goose Up
-- Consecutive verdicts missing a variant back off their rechecks, so artwork
-- that stays undeliverable cannot crowd out the rest of the catalog.
ALTER TABLE artwork_revision_gc_candidates
    ADD COLUMN IF NOT EXISTS delivery_failures integer NOT NULL DEFAULT 0;

-- Publication clears delivery_checked_at. The verifier claims those revisions
-- before routine rechecks, so a repaired or new upload is not queued behind the
-- whole catalog. A failed concurrent build leaves an invalid index, so drop
-- before building.
DROP INDEX CONCURRENTLY IF EXISTS artwork_delivery_pending_idx;
CREATE INDEX CONCURRENTLY artwork_delivery_pending_idx
ON artwork_revision_gc_candidates (delivery_next_check, id)
WHERE deleted_at IS NULL AND cardinality(coalesce(published_keys, object_keys)) > 0
  AND delivery_checked_at IS NULL;

-- Publication used to keep the previous verdict, so an incomplete verdict may
-- predate a successful re-upload. Recheck those ahead of the routine backlog.
UPDATE artwork_revision_gc_candidates
SET delivery_next_check = '-infinity'
WHERE deleted_at IS NULL AND delivery_checked_at IS NOT NULL
  AND cardinality(coalesce(published_keys, object_keys)) > 0
  AND NOT coalesce(published_keys, object_keys) <@ delivery_keys;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS artwork_delivery_pending_idx;
ALTER TABLE artwork_revision_gc_candidates DROP COLUMN IF EXISTS delivery_failures;
