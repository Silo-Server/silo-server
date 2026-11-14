-- "Trending on Server" was the default home trending row until the default
-- rows were reworked, so servers set up before then still store it. The
-- title-key backfill keyed only the current defaults' text; this keys the
-- old default's rows the same way: home rows of the trending type that still
-- hold its exact text. A row an administrator renamed, or another row that
-- happens to share the text, stays unkeyed.

-- +goose Up
UPDATE page_sections
SET title_key = 'home.section.trendingOnServer'
WHERE scope = 'home'
  AND library_id IS NULL
  AND section_type = 'trending_on_server'
  AND title = 'Trending on Server'
  AND title_key = '';

-- +goose Down
-- The key is a no-op for older binaries, and Up cannot tell which keyed rows
-- it changed, so Down leaves them as they are.
SELECT 1;
