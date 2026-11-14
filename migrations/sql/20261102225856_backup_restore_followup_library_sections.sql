-- +goose Up
-- A restored library's default sections are seeded once the restore wrote
-- the saved sections it carries, as their own follow-up.
ALTER TABLE public.backup_restore_followups DROP CONSTRAINT IF EXISTS backup_restore_followups_kind_check;
ALTER TABLE public.backup_restore_followups ADD CONSTRAINT backup_restore_followups_kind_check
    CHECK (kind IN ('library_created', 'library_sections', 'plugin_installed', 'collection_created'));

-- +goose Down
DELETE FROM public.backup_restore_followups WHERE kind = 'library_sections';
ALTER TABLE public.backup_restore_followups DROP CONSTRAINT IF EXISTS backup_restore_followups_kind_check;
ALTER TABLE public.backup_restore_followups ADD CONSTRAINT backup_restore_followups_kind_check
    CHECK (kind IN ('library_created', 'plugin_installed', 'collection_created'));
