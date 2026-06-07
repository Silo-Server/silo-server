-- +goose Up
-- +goose StatementBegin
-- Smart Collections — rule-based dynamic groupings of audiobooks.
-- The query_def JSONB column stores the DSL tree (see
-- internal/audiobooks/smartcoll/query.go). Profile-scoped per the
-- established convention; is_public allows cross-user reads with
-- personalization stripped at eval time.

CREATE TABLE IF NOT EXISTS public.abs_smart_collections (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    color       text NOT NULL DEFAULT '',
    is_public   boolean NOT NULL DEFAULT false,
    is_pinned   boolean NOT NULL DEFAULT false,
    query_def   jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_smart_collections_user_profile_idx
    ON public.abs_smart_collections (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.abs_smart_collections_user_profile_idx;
DROP TABLE IF EXISTS public.abs_smart_collections;
-- +goose StatementEnd
