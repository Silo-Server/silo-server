-- +goose Up
-- user_id and profile_id are functionally dependent: a profile belongs to
-- exactly one user, so (user_id = $1 AND profile_id = $2) selects the same rows
-- profile_id alone would. The planner does not know that. It multiplies the two
-- selectivities as if they were independent -- roughly (1/1300) * (1/1300) over
-- a few million rows -- and estimates one row where the real answer is
-- thousands.
--
-- Continue Watching pays for that estimate. Believing the outer side yields a
-- single row, the planner reads a rescan of the profile's whole hidden-history
-- slice as free, so it declines to push media_item_id into the inner index
-- condition and picks a nested-loop anti join against
-- user_history_hidden_items. It also scans idx_user_watch_progress_profile and
-- filters for completed, rather than reading the partial index that already
-- holds only completed rows. Measured on production data for one profile with
-- 3384 completed rows and 189 hidden rows: 639576 rows discarded by the join
-- filter, 372553 buffers, 251ms.
--
-- Extended statistics teach the planner the dependency. The same query then
-- plans as a hash anti join over idx_uwp_profile_completed_cursor: 26 rows
-- discarded, 1986 buffers, 4.0ms -- 62.7x faster, 188x fewer buffers. The top-N
-- sort stays; the win is the join method and the index, not the ordering.
--
-- Verified across ten production profiles, best of three passes, using the
-- column list ListProgress actually selects. The completed-history walk
-- improves on nine of the ten, 1.1x to 62.7x. Three of the twenty queries
-- measured get slower, all of them fast in absolute terms and all reproducible
-- across passes: on one profile the walk goes 0.43ms to 6.30ms and its resume
-- page 0.23ms to 1.49ms, because correct estimates make the planner hash all
-- 5635 completed rows and top-N sort instead of walking the ordered index and
-- stopping at 500; on another the resume page goes 0.43ms to 7.48ms. Continue
-- Watching as a whole is still faster on every profile measured -- the
-- request-scoped cache in the following commit removes far more work than these
-- plans add -- so the two commits belong together and should not be split.
--
-- These objects cover user_id and profile_id by name. Postgres drops an
-- extended statistics object silently, with no error, when a column it covers
-- is dropped -- so a later migration that drops either column takes the query
-- back to the nested loop with nothing failing to announce it. Renames and type
-- changes are safe: the object follows a RENAME and survives an ALTER TYPE
-- (its sampled data resets and the next ANALYZE refills it).
CREATE STATISTICS IF NOT EXISTS uwp_user_profile (dependencies, ndistinct)
    ON user_id, profile_id FROM public.user_watch_progress;

CREATE STATISTICS IF NOT EXISTS uhhi_user_profile (dependencies, ndistinct)
    ON user_id, profile_id FROM public.user_history_hidden_items;

-- A statistics object is inert until a sample exists for it. Without this the
-- fix lands whenever autoanalyze next happens to visit the table, which on a
-- table this size can be hours. Both ANALYZE runs sample rather than scan and
-- take well under a second each on a multi-million-row table.
ANALYZE public.user_watch_progress;
ANALYZE public.user_history_hidden_items;

-- +goose Down
DROP STATISTICS IF EXISTS uhhi_user_profile;
DROP STATISTICS IF EXISTS uwp_user_profile;
