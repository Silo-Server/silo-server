-- +goose Up
-- +goose StatementBegin
-- user_history_hidden_items now carries two cutoffs per profile and title.
--
-- hidden_before is the watched-state cutoff, set by mark unwatched and by
-- "Remove from history": progress and completed plays at or before it no
-- longer count as watched or in progress.
--
-- removed_before is the history-listing cutoff, set only by "Remove from
-- history": plays at or before it leave the profile's history list. Mark
-- unwatched leaves it alone, so the plays stay listed. NULL marks a row written
-- before this column existed, when both actions deleted the history; it reads
-- as hidden_before. A row an unmark creates stores -infinity.
--
-- The column is nullable with no default, so adding it rewrites nothing.
ALTER TABLE public.user_history_hidden_items
    ADD COLUMN removed_before timestamp with time zone;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.user_history_hidden_items
    DROP COLUMN IF EXISTS removed_before;
-- +goose StatementEnd
