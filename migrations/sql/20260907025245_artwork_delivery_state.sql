-- +goose Up
-- Keep successful publication separate from the GC manifest, which is emptied
-- before a retry uploads and may include objects that never finished uploading.
-- NULL means a legacy GC-only record; an empty array means publication began
-- but has not completed. Displacement triggers must keep the NULL default.
ALTER TABLE artwork_revision_gc_candidates
    ADD COLUMN published_keys text[],
    ADD COLUMN delivery_keys text[] NOT NULL DEFAULT '{}',
    ADD COLUMN delivery_scope text NOT NULL DEFAULT '',
    ADD COLUMN delivery_checked_at timestamptz,
    ADD COLUMN delivery_next_check timestamptz NOT NULL DEFAULT NOW(),
    ADD COLUMN delivery_lease text NOT NULL DEFAULT '';
-- Historical nonempty GC manifests could be recorded before upload. The
-- background verifier establishes publication only after checking storage.
CREATE INDEX artwork_delivery_due_idx ON artwork_revision_gc_candidates (delivery_next_check, id)
WHERE deleted_at IS NULL AND cardinality(coalesce(published_keys, object_keys)) > 0;

-- +goose Down
DROP INDEX artwork_delivery_due_idx;
ALTER TABLE artwork_revision_gc_candidates
    DROP COLUMN published_keys,
    DROP COLUMN delivery_keys,
    DROP COLUMN delivery_scope,
    DROP COLUMN delivery_checked_at,
    DROP COLUMN delivery_next_check,
    DROP COLUMN delivery_lease;
