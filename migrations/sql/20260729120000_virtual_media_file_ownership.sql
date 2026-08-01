-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_files
    ADD COLUMN IF NOT EXISTS virtual_owner_installation_id bigint;

UPDATE public.media_files mf
SET virtual_owner_installation_id = COALESCE(mi.virtual_owner_installation_id, 0)
FROM public.media_items mi
WHERE mi.content_id = mf.content_id
  AND mf.virtual_owner_installation_id IS NULL
  AND (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%');

ALTER TABLE ONLY public.media_files
    DROP CONSTRAINT IF EXISTS media_files_file_path_key;

CREATE UNIQUE INDEX IF NOT EXISTS media_files_local_file_path_key
    ON public.media_files (file_path)
    WHERE virtual_owner_installation_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS media_files_virtual_file_owner_key
    ON public.media_files (file_path, virtual_owner_installation_id)
    WHERE virtual_owner_installation_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_media_files_virtual_owner
    ON public.media_files (virtual_owner_installation_id)
    WHERE virtual_owner_installation_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_files_virtual_owner;
DROP INDEX IF EXISTS public.media_files_virtual_file_owner_key;
DROP INDEX IF EXISTS public.media_files_local_file_path_key;
ALTER TABLE ONLY public.media_files
    ADD CONSTRAINT media_files_file_path_key UNIQUE (file_path);
ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS virtual_owner_installation_id;
-- +goose StatementEnd
