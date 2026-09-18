-- +goose Up
-- +goose StatementBegin
-- Libraries created after migration 124 did not receive the placement group
-- that exposes opted-in personal collections in the shared library view.
-- Preserve regular groups that already use the reserved label or slug. Their
-- opaque IDs remain unchanged so existing collection memberships stay valid.
UPDATE library_collection_groups collision
SET
    label = 'user-collections-regular-' || collision.id,
    slug = 'user-collections-regular-' || collision.id,
    updated_at = NOW()
WHERE collision.kind = 'regular'
  AND (collision.label = 'user-collections' OR collision.slug = 'user-collections')
  AND NOT EXISTS (
      SELECT 1
      FROM library_collection_groups existing
      WHERE existing.library_id = collision.library_id
        AND existing.kind = 'user_collections'
  );

INSERT INTO library_collection_groups (
    library_id, label, title, sort_order, id, name, slug, kind, default_sort_mode
)
SELECT
    mf.id,
    'user-collections',
    'My collections',
    9998,
    'lcg_user_' || mf.id::text,
    'My collections',
    'user-collections',
    'user_collections',
    'manual'
FROM media_folders mf
WHERE NOT EXISTS (
    SELECT 1
    FROM library_collection_groups existing
    WHERE existing.library_id = mf.id
      AND existing.kind = 'user_collections'
)
ON CONFLICT (library_id, label) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- Intentionally a no-op: a rollback cannot distinguish groups inserted by
-- this repair from groups created with a library after the fix was deployed.
SELECT 1;
