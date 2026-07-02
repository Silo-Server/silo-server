-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.access_groups
    ADD COLUMN is_default boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX access_groups_one_default_idx
    ON public.access_groups(is_default)
    WHERE is_default;

INSERT INTO public.access_groups (
    name,
    description,
    is_default,
    library_ids,
    max_playback_quality,
    download_allowed,
    download_transcode_allowed,
    max_streams,
    max_transcodes,
    allowed_permissions,
    requests_allowed
)
SELECT
    'Default Group',
    'Applied automatically to newly created users.',
    true,
    NULL,
    '',
    true,
    true,
    0,
    0,
    NULL,
    true
WHERE NOT EXISTS (
    SELECT 1
    FROM public.access_groups
    WHERE is_default
)
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM public.access_groups
WHERE name = 'Default Group'
  AND description = 'Applied automatically to newly created users.'
  AND is_default
  AND library_ids IS NULL
  AND max_playback_quality = ''
  AND download_allowed
  AND download_transcode_allowed
  AND max_streams = 0
  AND max_transcodes = 0
  AND allowed_permissions IS NULL
  AND requests_allowed;

DROP INDEX IF EXISTS public.access_groups_one_default_idx;

ALTER TABLE public.access_groups
    DROP COLUMN IF EXISTS is_default;
-- +goose StatementEnd
