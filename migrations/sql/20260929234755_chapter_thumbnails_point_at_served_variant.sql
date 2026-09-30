-- +goose Up
-- Chapter thumbnails used to be stored twice, as a full-size original.webp and
-- a 300px w300.webp. thumbnail_path held the original, and every reader
-- rewrote it to w300 before signing a URL. New thumbnails store only w300.webp
-- and thumbnail_path names it directly, so point existing rows at the w300
-- object they were already served from. Both objects were written before the
-- path was saved, so w300.webp exists wherever original.webp was recorded.
--
-- The artwork reconciler checks thumbnail_path, so this also moves its check
-- onto the object clients actually load. The one-time cleanup task deletes the
-- orphaned original.webp objects only after this has run.
UPDATE media_files
SET chapters = (
    SELECT jsonb_agg(
        CASE
            WHEN e->>'thumbnail_path' ~ '^chapter-images/[0-9]+/[0-9]+/original\.webp$'
            THEN jsonb_set(e, '{thumbnail_path}',
                           to_jsonb(regexp_replace(e->>'thumbnail_path', '/original\.webp$', '/w300.webp')))
            ELSE e
        END
        ORDER BY ord
    )
    FROM jsonb_array_elements(chapters) WITH ORDINALITY AS t(e, ord)
)
WHERE jsonb_typeof(chapters) = 'array'
  AND EXISTS (
      SELECT 1
      FROM jsonb_array_elements(chapters) e
      WHERE e->>'thumbnail_path' ~ '^chapter-images/[0-9]+/[0-9]+/original\.webp$'
  );

-- +goose Down
-- Intentionally empty. Earlier builds rewrite /original. to /w300. before
-- serving and leave a w300 path unchanged, so they read the new paths
-- correctly. Pointing rows back at original.webp would reference objects the
-- cleanup task may already have deleted.
SELECT 1;
