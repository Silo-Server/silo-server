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
    false,
    5,
    5,
    ARRAY['marker_edit'],
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
  AND NOT download_transcode_allowed
  AND max_streams = 5
  AND max_transcodes = 5
  AND allowed_permissions = ARRAY['marker_edit']
  AND requests_allowed;

DROP INDEX IF EXISTS public.access_groups_one_default_idx;

ALTER TABLE public.access_groups
    DROP COLUMN IF EXISTS is_default;
-- +goose StatementEnd
