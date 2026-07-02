-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.access_groups (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    library_ids integer[],
    max_playback_quality text NOT NULL DEFAULT '',
    download_allowed boolean NOT NULL DEFAULT true,
    download_transcode_allowed boolean NOT NULL DEFAULT true,
    max_streams integer NOT NULL DEFAULT 0,
    max_transcodes integer NOT NULL DEFAULT 0,
    allowed_permissions text[],
    requests_allowed boolean NOT NULL DEFAULT true,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

ALTER TABLE public.users
    ADD COLUMN access_group_id bigint
    REFERENCES public.access_groups(id) ON DELETE SET NULL;

CREATE INDEX idx_users_access_group_id
    ON public.users(access_group_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_users_access_group_id;

ALTER TABLE public.users
    DROP COLUMN IF EXISTS access_group_id;

DROP TABLE IF EXISTS public.access_groups;
-- +goose StatementEnd
