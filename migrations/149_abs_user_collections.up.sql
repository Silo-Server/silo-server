-- Manual user collections (named groupings of audiobooks).
-- Profile-scoped: NULL profile_id encodes the primary profile, and the
-- COALESCE-to-sentinel-UUID trick in the lookup index collapses NULL
-- to a single bucket per user (raw NULL is treated as distinct for
-- index purposes otherwise).
--
-- is_public allows other users on the same silo instance to GET-by-id
-- (the list endpoint never exposes other users' collections; only the
-- detail route honors is_public).

CREATE TABLE IF NOT EXISTS public.abs_user_collections (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    is_public   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_user_collections_user_profile_idx
    ON public.abs_user_collections (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
