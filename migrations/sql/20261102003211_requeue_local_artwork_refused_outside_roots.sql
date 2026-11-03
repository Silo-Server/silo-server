-- +goose Up
-- Local artwork in a title folder that is a symlink out of the library is now
-- read when a cataloged file of the title in that folder vouches for it (see
-- docs/architecture/local-nfo-metadata.md). Jobs refused before as outside
-- the library roots wait a week between attempts and, once their attempts are
-- spent, park for good; enqueue reopens a parked job only when its source
-- path changes. Try them again now. A path that is still outside the roots is
-- refused and parks again.
UPDATE public.metadata_image_cache_jobs
SET status = 'queued',
    attempt_count = 0,
    next_attempt_at = now(),
    locked_at = NULL,
    locked_by = '',
    last_error = '',
    updated_at = now()
WHERE status IN ('queued', 'failed')
  AND source_path LIKE 'file://%'
  AND starts_with(last_error, 'local image path outside library roots');

-- +goose Down
-- Requeued jobs either cache their artwork or are refused and park again;
-- there is nothing to restore.
SELECT 1;
