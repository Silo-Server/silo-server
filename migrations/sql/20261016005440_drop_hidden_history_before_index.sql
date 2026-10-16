-- +goose NO TRANSACTION

-- +goose Up
-- Every read of user_history_hidden_items looks up one title's row: the
-- "progress written at or before hidden_before is hidden" check that progress,
-- history, Continue Watching, and Next Up queries run per row. The primary key
-- (user_id, profile_id, media_item_id) answers that with one index probe.
--
-- idx_user_history_hidden_items_profile (user_id, profile_id, hidden_before
-- DESC) serves no query, but the planner keeps choosing it for that check: it
-- turns updated_at <= hidden_before into an index range and filters
-- media_item_id afterwards, so each probe reads every hidden row of the profile
-- newer than the progress row. Without the index the planner can only use the
-- primary key.
--
-- Measured on a copy of a development database, median of 30 runs, for a
-- profile with 576 hidden titles and 14k completed rows: the Continue Watching
-- next-up walk went from 406ms to 93ms, and a page of 500 completed rows at
-- offset 2000 from 47ms to 2.2ms. On profiles with up to 11k hidden titles the
-- other hidden-history reads (history page, in-progress page, watched joins)
-- stayed within 1ms of their times with the index.
--
-- The index is dropped concurrently so progress reads and writes continue.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_user_history_hidden_items_profile;

-- +goose Down
-- The rollback only runs after the index was dropped, so an index found here
-- is the invalid leftover of an interrupted rebuild. Drop it so the rebuild
-- runs again instead of keeping it.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_user_history_hidden_items_profile;

CREATE INDEX CONCURRENTLY idx_user_history_hidden_items_profile
    ON public.user_history_hidden_items (user_id, profile_id, hidden_before DESC);
