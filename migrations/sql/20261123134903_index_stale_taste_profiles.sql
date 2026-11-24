-- +goose NO TRANSACTION
-- +goose Up
-- The stale sweep reads the profiles with a stale mark a page at a time in
-- mark order, (stale_at, user_id, profile_id), after the last profile of the
-- previous page (Repo.GetStaleProfiles). Without an index each page reads and
-- sorts all of user_taste_profiles, so a large backlog costs a full scan per
-- page. Only marked rows are indexed, and the predicate is the query's own
-- stale_at IS NOT NULL, so the planner can use it.
--
-- CONCURRENTLY so this does not take a write lock on a running server, which
-- requires NO TRANSACTION. A failed nontransactional run can leave an invalid
-- index behind, which IF NOT EXISTS would keep, so the index is dropped
-- concurrently first and a retry rebuilds it, as in
-- 20261116235542_add_media_items_frame_poster_index.sql.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_user_taste_profiles_stale;
CREATE INDEX CONCURRENTLY idx_user_taste_profiles_stale ON public.user_taste_profiles (stale_at, user_id, profile_id) WHERE stale_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_user_taste_profiles_stale;
