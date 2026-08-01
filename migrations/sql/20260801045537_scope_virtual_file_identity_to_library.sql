-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS public.media_files_virtual_file_owner_key;

CREATE UNIQUE INDEX media_files_virtual_file_owner_key
    ON public.media_files (file_path, virtual_owner_installation_id, media_folder_id)
    WHERE virtual_owner_installation_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.media_files
        WHERE virtual_owner_installation_id IS NOT NULL
        GROUP BY file_path, virtual_owner_installation_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot restore global virtual file identity: the same provider path exists in multiple libraries';
    END IF;
END $$;

DROP INDEX IF EXISTS public.media_files_virtual_file_owner_key;

CREATE UNIQUE INDEX media_files_virtual_file_owner_key
    ON public.media_files (file_path, virtual_owner_installation_id)
    WHERE virtual_owner_installation_id IS NOT NULL;
-- +goose StatementEnd
