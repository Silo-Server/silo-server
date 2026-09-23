-- +goose NO TRANSACTION
-- +goose Up
-- Continue Watching, Next Up, Jellyfin Resume, and v2 GET /progress select a
-- profile's resume points with position_seconds > 0 (progressStatusPredicate
-- in internal/userstore/pgstore/progress.go). Since
-- 20260609224115_reset_completed_progress_position a completed row holds
-- position 0, and a rewatch keeps completed = TRUE with a live position.
-- idx_uwp_profile_in_progress is partial on completed = FALSE, which that
-- predicate does not imply, so the planner could not use it for those lists:
-- each one read every progress row of the profile and sorted the few that
-- survived. This index carries the list predicate and its updated_at order, so
-- a page reads only the profile's resume points and stops at its LIMIT.
--
-- Measured on a synthetic profile with 19,700 progress rows, 600 of them
-- resumable, in a 121,700-row table: a Continue Watching page (LIMIT 100) went
-- from a bitmap scan of all 19,700 rows plus a sort (1,759 shared buffers,
-- 8.7 ms median) to an ordered index scan of 100 rows (202 buffers, 0.25 ms).
--
-- idx_uwp_profile_in_progress goes. Every remaining query whose filter implies
-- completed = FALSE either looks its row up by primary key or also requires
-- position_seconds > 0, which this index serves with fewer rows.
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
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_uwp_profile_in_progress
ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC)
WHERE completed = FALSE;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_uwp_profile_resumable;
