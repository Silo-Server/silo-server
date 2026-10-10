-- +goose NO TRANSACTION
-- +goose Up
-- The download refresh pass, and its idle check on every API node, look for
-- downloading targets that have progress. Only those few rows are indexed, so
-- neither has to scan a request history. A failed concurrent build leaves an
-- invalid index, so drop before building.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_request_targets_download_refresh;
CREATE INDEX CONCURRENTLY idx_media_request_targets_download_refresh
ON public.media_request_targets USING btree (request_id, download_checked_at)
WHERE status = 'downloading' AND download_phase IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_request_targets_download_refresh;
