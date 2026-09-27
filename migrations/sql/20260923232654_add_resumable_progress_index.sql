-- +goose NO TRANSACTION
-- +goose Up
-- Continue Watching, Jellyfin Resume, v2 GET /progress?status=in_progress,
-- Next Up's resumable list, and the episode catalog's in_progress rule select a
-- profile's resume points with position_seconds > 0 (progressStatusPredicate
-- in internal/userstore/pgstore/progress.go). Since
-- 20260609224115_reset_completed_progress_position a completed row holds
-- position 0, and a rewatch keeps completed = TRUE with a live position.
-- idx_uwp_profile_in_progress is partial on completed = FALSE, which that
-- predicate does not imply, so the planner could not use it for those lists:
-- each one read every progress row of the profile and kept the few with a
-- position. This index carries the list predicate and its updated_at order, so
-- a list reads only the profile's resume points, and a Continue Watching page
-- reads them newest first and stops at its LIMIT.
--
-- Measured on a synthetic profile with 20,752 progress rows, 509 of them
-- resumable, in a 124,452-row table: a Continue Watching page (LIMIT 100) went
-- from a bitmap scan of all 20,752 rows plus a top-N sort (1,799 shared
-- buffers, about 7 ms) to an ordered index scan of 100 rows with no sort (200
-- buffers, about 0.2 ms).
--
-- idx_uwp_profile_in_progress goes. Its only reader is the Jellyfin
-- IsResumable browse filter (internal/catalog/browse.go) when it plans as a
-- join over the profile's rows rather than a primary-key probe per item. That
-- filter also requires position_seconds > 0, so it moves to this index and
-- reads the profile's resume points instead of its unfinished rows. The other
-- completed = FALSE predicates fetch one row by primary key, guard an ON
-- CONFLICT update, or sit inside an aggregate FILTER. Audiobookshelf Continue
-- Listening writes COALESCE(completed, FALSE) = FALSE, which the planner
-- cannot match to a partial index on completed = FALSE.
--
-- CONCURRENTLY keeps user_watch_progress writable during the build; it cannot
-- run inside a transaction, hence NO TRANSACTION. An interrupted concurrent
-- build leaves an invalid index that IF NOT EXISTS would keep, so drop it first.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_uwp_profile_resumable'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_uwp_profile_resumable;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_uwp_profile_resumable
ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC)
WHERE position_seconds > 0;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_uwp_profile_in_progress;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_uwp_profile_in_progress'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_uwp_profile_in_progress;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_uwp_profile_in_progress
ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC)
WHERE completed = FALSE;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_uwp_profile_resumable;
