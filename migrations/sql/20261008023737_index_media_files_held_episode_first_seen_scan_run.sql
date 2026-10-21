-- +goose NO TRANSACTION

-- +goose Up
-- held_episode_first_seen_scan_run_id references scan_runs ON DELETE SET NULL.
-- Deleting a library cascades to every scan run it has ever recorded, and each
-- deleted run makes PostgreSQL look up the media_files rows that still point
-- at it. Without an index every one of those lookups scans all of media_files.
-- Only held rows carry a value, so the index stays small.

-- A failed concurrent build can leave an invalid index. Keep retry cleanup
-- outside a transaction so it cannot take a blocking ordinary index-drop lock.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_held_episode_first_seen_scan_run_id;

CREATE INDEX CONCURRENTLY idx_media_files_held_episode_first_seen_scan_run_id
    ON public.media_files (held_episode_first_seen_scan_run_id)
    WHERE held_episode_first_seen_scan_run_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_held_episode_first_seen_scan_run_id;
