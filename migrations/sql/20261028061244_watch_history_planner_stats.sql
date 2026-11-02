-- +goose Up
-- 20260904144457_continue_watching_planner_stats taught the planner that
-- user_id and profile_id are functionally dependent on user_watch_progress and
-- user_history_hidden_items. user_watch_history has the same dependency and
-- was left out. Without it the planner multiplies the two selectivities as if
-- they were independent and estimates a handful of rows for a profile that has
-- thousands: on production data, 11 estimated against 8986 completed rows for
-- one profile, and 1 against 7661 for another.
--
-- The series and season watched rollup (episodeParentCompletion) pays for that
-- estimate. Believing the profile's completed history is a few rows, the
-- planner materializes all of it once and rescans the whole list for every
-- episode of every series on the card set: 134398 rows discarded by the join
-- filter per series, about 24 million in all, 1.4-3.0s for a Home-sized set of
-- about 180 series. With the dependency known it probes
-- idx_user_watch_history_profile_item_completed per episode instead.
--
-- Measured on production data with the unchanged query, about 180 series per
-- profile, best of two passes:
--   completed history  before    after
--        1.5k          248ms     105ms
--        7.7k         1405ms      66ms
--        9.0k         3050ms     160ms
--        288k           84ms      76ms
-- The 288k profile was already fast because its user_id is common enough to
-- sit in the per-column most-common-values list; the bad estimate hits the
-- mid-sized histories that do not.
--
-- As with the earlier object, Postgres drops an extended statistics object
-- silently when a column it covers is dropped, so a later migration that drops
-- either column takes these queries back to the slow plan with nothing failing
-- to announce it. Renames and type changes are safe.
CREATE STATISTICS IF NOT EXISTS uwh_user_profile (dependencies, ndistinct)
    ON user_id, profile_id FROM public.user_watch_history;

-- A statistics object is inert until a sample exists for it. ANALYZE samples
-- rather than scans; it took about a second on a 7.4M-row table.
ANALYZE public.user_watch_history;

-- +goose Down
DROP STATISTICS IF EXISTS uwh_user_profile;
