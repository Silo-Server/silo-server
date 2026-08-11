-- +goose NO TRANSACTION
-- +goose Up
-- The global Next Up anchor walk advances on a compound
-- (updated_at, media_item_id) cursor: each step asks for the newest completed
-- row strictly before the previous anchor's pair. idx_uwp_profile_completed is
-- (user_id, profile_id, updated_at DESC) only, so the media_item_id half of
-- that comparison is a filter rather than part of the seek, and every step
-- re-reads the rows tied on updated_at — exactly the shape a bulk
-- mark-watched profile has, where hundreds of rows share one timestamp.
--
-- Adding media_item_id to the index makes each step a single ordered seek.
-- Measured by dropping and recreating this index around the shipped query on a
-- synthetic profile with a 40k-row bulk-marked series: 341ms -> 224ms (~34%),
-- same plan shape otherwise. The partial predicate matches the existing
-- completed-only index so the two stay interchangeable for planning; this one
-- supersedes it for the walk.
--
-- CONCURRENTLY avoids locking user_watch_progress writes during the build; it
-- cannot run inside a transaction, hence the NO TRANSACTION annotation above.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_uwp_profile_completed_cursor
ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC, media_item_id DESC)
WHERE completed = TRUE;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_uwp_profile_completed_cursor;
