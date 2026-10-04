-- +goose Up
-- +goose StatementBegin
-- subtitle_upload is granted by default so existing accounts keep uploading.
-- Admins are backfilled too: an acting admin holds it implicitly, but an
-- admin's non-primary profiles need it assigned, and they could upload before.
-- A group with a null allowed_permissions already allows every assignable
-- permission.
ALTER TABLE public.users
    ALTER COLUMN permissions SET DEFAULT ARRAY['marker_edit', 'subtitle_upload']::text[];

UPDATE public.users
SET permissions = array_append(permissions, 'subtitle_upload')
WHERE array_position(permissions, 'subtitle_upload') IS NULL;

UPDATE public.access_groups
SET allowed_permissions = array_append(allowed_permissions, 'subtitle_upload')
WHERE allowed_permissions IS NOT NULL
  AND array_position(allowed_permissions, 'subtitle_upload') IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE public.access_groups
SET allowed_permissions = array_remove(allowed_permissions, 'subtitle_upload')
WHERE allowed_permissions IS NOT NULL;

UPDATE public.users
SET permissions = array_remove(permissions, 'subtitle_upload');

ALTER TABLE public.users
    ALTER COLUMN permissions SET DEFAULT ARRAY['marker_edit']::text[];
-- +goose StatementEnd
