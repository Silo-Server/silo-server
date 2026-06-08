-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.users
    ALTER COLUMN permissions SET DEFAULT ARRAY['marker_edit']::text[];

UPDATE public.users
SET permissions = array_append(permissions, 'marker_edit')
WHERE role <> 'admin'
  AND NOT ('marker_edit' = ANY (permissions));

CREATE TABLE public.marker_edit_audit (
    id                   bigserial PRIMARY KEY,
    media_file_id        integer NOT NULL REFERENCES public.media_files(id) ON DELETE CASCADE,
    segment_kind         text    NOT NULL,
    action               text    NOT NULL,
    before_marker        jsonb,
    after_marker         jsonb,
    user_id              integer REFERENCES public.users(id) ON DELETE SET NULL,
    impersonator_user_id integer REFERENCES public.users(id) ON DELETE SET NULL,
    api_key_id           bigint REFERENCES public.api_keys(id) ON DELETE SET NULL,
    request_id           text,
    client_ip            inet,
    user_agent           text,
    created_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT marker_edit_audit_segment_kind_check
        CHECK (segment_kind = ANY (ARRAY['intro'::text, 'credits'::text, 'recap'::text, 'preview'::text])),
    CONSTRAINT marker_edit_audit_action_check
        CHECK (action = ANY (ARRAY['set'::text, 'clear'::text]))
);

CREATE INDEX marker_edit_audit_file_created_idx
    ON public.marker_edit_audit (media_file_id, created_at DESC, id DESC);

CREATE INDEX marker_edit_audit_user_created_idx
    ON public.marker_edit_audit (user_id, created_at DESC, id DESC)
    WHERE user_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.marker_edit_audit;

UPDATE public.users
SET permissions = array_remove(permissions, 'marker_edit')
WHERE role <> 'admin';

ALTER TABLE public.users
    ALTER COLUMN permissions SET DEFAULT '{}'::text[];
-- +goose StatementEnd
