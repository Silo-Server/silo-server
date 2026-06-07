-- +goose Up
-- +goose StatementBegin
-- Ordered audiobook playlists. Profile-scoped (NULL profile_id =
-- primary profile, collapsed to a single bucket per user via the
-- COALESCE-to-sentinel index trick).
--
-- cover_item references media_items(content_id) with ON DELETE SET NULL
-- so the playlist survives cover-item deletion gracefully.

CREATE TABLE IF NOT EXISTS public.abs_playlists (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    cover_item  text REFERENCES public.media_items(content_id) ON DELETE SET NULL,
    is_public   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_playlists_user_profile_idx
    ON public.abs_playlists (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.abs_playlists_user_profile_idx;
DROP TABLE IF EXISTS public.abs_playlists;
-- +goose StatementEnd
