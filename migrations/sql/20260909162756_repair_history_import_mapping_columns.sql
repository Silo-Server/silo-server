-- +goose Up
-- Older installations retain the pre-rename column names from migration 049.
-- Rename in place to preserve mapping identities, revisions, indexes and FKs.
-- +goose StatementBegin
DO $$
DECLARE
    old_name text;
    new_name text;
BEGIN
    FOR old_name, new_name IN
        SELECT * FROM (VALUES
            ('continuum_user_id', 'silo_user_id'),
            ('continuum_profile_id', 'silo_profile_id')
        ) AS names(old_name, new_name)
    LOOP
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'history_import_user_mappings'
              AND column_name = old_name
        ) THEN
            IF EXISTS (
                SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'history_import_user_mappings'
                  AND column_name = new_name
            ) THEN
                RAISE EXCEPTION 'history_import_user_mappings has both % and %; reconcile mappings before migrating', old_name, new_name;
            END IF;
            EXECUTE format('ALTER TABLE public.history_import_user_mappings RENAME COLUMN %I TO %I', old_name, new_name);
        END IF;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Compatibility repair: the previous application also requires silo_* names.
-- Keep the repaired names on rollback rather than breaking existing mappings.
SELECT 1;
