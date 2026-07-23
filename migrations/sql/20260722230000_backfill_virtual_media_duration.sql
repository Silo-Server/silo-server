-- +goose Up
-- Virtual rows created before RuntimeHost carried canonical runtimes had no
-- duration. Use already-enriched catalog metadata so existing installations
-- receive a stable seek bar without requiring users to request titles again.
UPDATE media_files AS mf
SET duration = COALESCE(
    (SELECT NULLIF(e.runtime, 0) * 60 FROM episodes AS e WHERE e.content_id = mf.episode_id),
    (SELECT NULLIF(mi.runtime, 0) * 60 FROM media_items AS mi WHERE mi.content_id = mf.content_id)
)
WHERE lower(COALESCE(mf.container, '')) = 'virtual'
  AND COALESCE(mf.duration, 0) <= 0
  AND COALESCE(
      (SELECT NULLIF(e.runtime, 0) FROM episodes AS e WHERE e.content_id = mf.episode_id),
      (SELECT NULLIF(mi.runtime, 0) FROM media_items AS mi WHERE mi.content_id = mf.content_id)
  ) IS NOT NULL;

-- +goose Down
-- This is a metadata backfill. The previous absence of a duration cannot be
-- distinguished from values subsequently learned by normal metadata refresh.
SELECT 1;
