-- +goose Up
-- A restored template collection owes follow-up work too: its template
-- poster and its first sync. Its id is the collection's, minted numeric.
ALTER TABLE public.backup_restore_followups DROP CONSTRAINT IF EXISTS backup_restore_followups_kind_check;
ALTER TABLE public.backup_restore_followups ADD CONSTRAINT backup_restore_followups_kind_check
    CHECK (kind IN ('library_created', 'plugin_installed', 'collection_created'));

-- +goose Down
DELETE FROM public.backup_restore_followups WHERE kind = 'collection_created';
ALTER TABLE public.backup_restore_followups DROP CONSTRAINT IF EXISTS backup_restore_followups_kind_check;
ALTER TABLE public.backup_restore_followups ADD CONSTRAINT backup_restore_followups_kind_check
    CHECK (kind IN ('library_created', 'plugin_installed'));
