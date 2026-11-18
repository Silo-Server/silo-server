-- +goose Up
-- A catalog search rebuild now keeps 16 Meilisearch document tasks queued
-- instead of 4, so Meilisearch indexes larger batches. Every database stored
-- the old default 4 as a row, which would override the new default; move rows
-- still at 4. The setting has no control in the web app, so a 4 is almost
-- always the seeded default rather than an administrator's choice. Any other
-- value stays.
UPDATE server_settings
SET value = '16'
WHERE key = 'catalog.search.meilisearch.rebuild_task_queue_depth'
  AND value = '4';

-- +goose Down
-- Intentionally a no-op: after the update there is no reliable way to
-- distinguish the migrated default from an operator-chosen 16.
SELECT 1;
