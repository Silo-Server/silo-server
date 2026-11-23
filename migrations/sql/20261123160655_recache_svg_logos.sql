-- +goose Up
-- The artwork cache drew SVG logos at their declared canvas, which for TMDB
-- logos is a few dozen units wide, so every cached rung of such a logo was a
-- 54px image that clients stretched across the title area. imageutil now
-- draws an SVG at the cache's original width; this repairs logos already
-- cached the old way.
--
-- The job that cached each one, where it still exists, is queued again so
-- the image cache re-renders it with the fix. The jobs wait an hour, so a
-- node still on the old version during a rolling upgrade does not claim one
-- and cache the tiny image again.
--
-- A logo from a metadata provider also goes back to its provider source, so
-- clients get the provider's SVG straight away, as on a server that does not
-- cache artwork. Jobs that succeeded more than 30 days ago have been pruned;
-- those logos are cached again by "Backfill Metadata Images" or the item's
-- next metadata refresh. Logos from library files, NFOs, and other
-- non-provider sources keep their cached path, because clients cannot load
-- those sources; without a job left to re-queue, they stay as they are until
-- the item is scanned or refreshed again. The excluded schemes are the
-- image cache's nonProviderImageSchemesSQL.
UPDATE metadata_image_cache_jobs j
SET status = 'queued',
    attempt_count = 0,
    next_attempt_at = NOW() + interval '1 hour',
    locked_at = NULL,
    locked_by = '',
    last_error = '',
    completed_at = NULL,
    updated_at = NOW()
WHERE j.image_type = 'logo'
  AND j.status IN ('succeeded', 'failed')
  -- A library file's source ends in its "?v=<size>-<mtime>" fingerprint
  -- (localImageFingerprint in internal/metadata).
  AND lower(j.source_path) ~ '\.svg(\?v=\d+--?\d+)?$'
  AND (
      (j.target_type = 'item' AND EXISTS (
          SELECT 1 FROM media_items mi
          WHERE mi.content_id = j.target_content_id
            AND mi.logo_source_path = j.source_path))
      OR (j.target_type = 'item_localization' AND EXISTS (
          SELECT 1 FROM media_item_localizations loc
          WHERE loc.content_id = j.target_content_id
            AND loc.language = j.target_language
            AND loc.logo_source_path = j.source_path))
  );

UPDATE media_items
SET logo_path = logo_source_path,
    updated_at = NOW()
WHERE lower(logo_source_path) LIKE '%.svg'
  AND logo_source_path LIKE '%://%'
  AND lower(logo_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
  AND coalesce(logo_path, '') <> ''
  AND logo_path NOT LIKE '%://%';

UPDATE media_item_localizations
SET logo_path = logo_source_path,
    updated_at = NOW()
WHERE lower(logo_source_path) LIKE '%.svg'
  AND logo_source_path LIKE '%://%'
  AND lower(logo_source_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'nfo+%', 'local://%', 'upload://%', 'generated://%'])
  AND coalesce(logo_path, '') <> ''
  AND logo_path NOT LIKE '%://%';

-- +goose Down
-- The repair only moves pointers back to sources the catalog already holds;
-- there is nothing to undo.
SELECT 1;
