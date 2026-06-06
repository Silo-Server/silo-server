-- Reverse migration 156. Lossy in reverse — ABS playlist rows created after
-- the up migration are deleted, not migrated back.

-- 1. Remove the page_sections column.
ALTER TABLE page_sections DROP COLUMN media_types;

-- 2. Recreate the abs_* tables empty. Schemas inlined from the
--    original up migrations 149–153 (constraint/index names match
--    originals so a subsequent up-down-up cycle is idempotent).

-- BEGIN inlined from migrations/149_abs_user_collections.up.sql
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
-- END inlined

-- BEGIN inlined from migrations/150_abs_collection_items.up.sql
CREATE TABLE IF NOT EXISTS public.abs_collection_items (
    collection_id   text NOT NULL REFERENCES public.abs_user_collections(id) ON DELETE CASCADE,
    library_item_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    added_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection_id, library_item_id)
);

CREATE INDEX IF NOT EXISTS abs_collection_items_library_item_idx
    ON public.abs_collection_items (library_item_id);
-- END inlined

-- BEGIN inlined from migrations/151_abs_playlists.up.sql
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
-- END inlined

-- BEGIN inlined from migrations/152_abs_playlist_items.up.sql
CREATE TABLE IF NOT EXISTS public.abs_playlist_items (
    playlist_id     text NOT NULL REFERENCES public.abs_playlists(id) ON DELETE CASCADE,
    library_item_id text NOT NULL,
    episode_id      text NOT NULL DEFAULT '',
    position        integer NOT NULL,
    added_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (playlist_id, library_item_id, episode_id)
);

CREATE INDEX IF NOT EXISTS abs_playlist_items_playlist_position_idx
    ON public.abs_playlist_items (playlist_id, position);
-- END inlined

-- BEGIN inlined from migrations/153_abs_smart_collections.up.sql
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
-- END inlined

-- 3. Remove rows we promoted from abs_playlists during the up. Smart personal
--    collections predate migration 156, so they must not be deleted here.
DELETE FROM user_personal_collection_items
    WHERE collection_id IN (
        SELECT id FROM user_personal_collections
        WHERE collection_type = 'playlist'
    );
DELETE FROM user_personal_collections
    WHERE collection_type = 'playlist';

-- 4. Remove the ABS-only playlist type while preserving the pre-156 personal
--    collection domain, including smart and import-backed collections.
ALTER TABLE user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
    ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'smart', 'mdblist', 'tmdb', 'trakt', 'synced'));

-- 5. Drop the sub_item_id column.
ALTER TABLE user_personal_collection_items
    DROP CONSTRAINT IF EXISTS user_personal_collection_items_pkey;
ALTER TABLE user_personal_collection_items
    ADD CONSTRAINT user_personal_collection_items_pkey
    PRIMARY KEY (user_id, collection_id, media_item_id);

ALTER TABLE user_personal_collection_items DROP COLUMN sub_item_id;
