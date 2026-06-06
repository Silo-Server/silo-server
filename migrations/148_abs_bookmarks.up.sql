-- ABS bookmark rows. One row per (user, profile, item, time). Backs the
-- POST/PATCH/DELETE /me/item/{itemId}/bookmark endpoints in
-- internal/audiobooks/abs/bookmarks_handler.go.
--
-- profile_id is nullable because silo's "primary profile" is encoded as
-- NULL profile. The COALESCE-to-sentinel-UUID in the unique index
-- collapses NULL to a single bucket per user (raw NULL would be treated
-- as distinct for uniqueness purposes).

CREATE TABLE IF NOT EXISTS public.abs_bookmarks (
    id              text PRIMARY KEY,
    user_id         integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id      uuid,
    library_item_id text NOT NULL,
    time_seconds    double precision NOT NULL,
    title           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS abs_bookmarks_user_profile_item_time_uniq
    ON public.abs_bookmarks (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
        library_item_id,
        time_seconds
    );

CREATE INDEX IF NOT EXISTS abs_bookmarks_user_item_idx
    ON public.abs_bookmarks (user_id, library_item_id);
