-- +goose Up
-- +goose StatementBegin
-- Unify user-owned lists into user_personal_collections.
--
-- See docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md
-- for design rationale.

-- 1. Sub-item granularity column on the canonical items table.
--    Empty string for whole-item entries; populated for podcast-episode
--    playlist entries (sub_item_id == abs_playlist_items.episode_id).
ALTER TABLE user_personal_collection_items
    ADD COLUMN sub_item_id text NOT NULL DEFAULT '';

ALTER TABLE user_personal_collection_items
    DROP CONSTRAINT IF EXISTS user_personal_collection_items_pkey;
ALTER TABLE user_personal_collection_items
    ADD CONSTRAINT user_personal_collection_items_pkey
    PRIMARY KEY (user_id, collection_id, media_item_id, sub_item_id);

-- 2. Widen the collection_type domain. Personal collections already support
--    smart and import-backed types; this migration adds ABS playlists without
--    narrowing existing rows.
ALTER TABLE user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
    ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'smart', 'mdblist', 'tmdb', 'trakt', 'synced', 'playlist'));

-- 3. Move existing ABS manual collections into the canonical store.
INSERT INTO user_personal_collections
    (id, user_id, profile_id, name, description, collection_type,
     is_shared, query_definition, created_at, updated_at, creator_profile_id)
SELECT
    id,
    user_id,
    COALESCE(profile_id::text, ''),
    name,
    description,
    'manual',
    is_public,
    '{}'::jsonb,
    created_at,
    updated_at,
    COALESCE(profile_id::text, '')
FROM abs_user_collections
ON CONFLICT (user_id, id) DO NOTHING;

INSERT INTO user_personal_collection_items
    (user_id, collection_id, media_item_id, sub_item_id, position, added_at)
SELECT
    c.user_id,
    i.collection_id,
    i.library_item_id,
    '',
    ROW_NUMBER() OVER (PARTITION BY i.collection_id ORDER BY i.added_at, i.library_item_id) - 1,
    i.added_at
FROM abs_collection_items i
JOIN abs_user_collections c ON c.id = i.collection_id
ON CONFLICT (user_id, collection_id, media_item_id, sub_item_id) DO NOTHING;

-- 4. Move existing ABS smart collections into the canonical store.
INSERT INTO user_personal_collections
    (id, user_id, profile_id, name, description, collection_type,
     is_shared, query_definition, created_at, updated_at, creator_profile_id)
SELECT
    id,
    user_id,
    COALESCE(profile_id::text, ''),
    name,
    description,
    'smart',
    is_public,
    COALESCE(query_def, '{}'::jsonb),
    created_at,
    updated_at,
    COALESCE(profile_id::text, '')
FROM abs_smart_collections
ON CONFLICT (user_id, id) DO NOTHING;

-- 5. Move the existing abs_playlists row(s) into the canonical store.
--    is_public maps to is_shared; profile_id (uuid) is stringified.
INSERT INTO user_personal_collections
    (id, user_id, profile_id, name, description, collection_type,
     is_shared, query_definition, created_at, updated_at, creator_profile_id)
SELECT
    id,
    user_id,
    COALESCE(profile_id::text, ''),
    name,
    description,
    'playlist',
    is_public,
    '{}'::jsonb,
    created_at,
    updated_at,
    COALESCE(profile_id::text, '')
FROM abs_playlists
ON CONFLICT (user_id, id) DO NOTHING;

INSERT INTO user_personal_collection_items
    (user_id, collection_id, media_item_id, sub_item_id, position, added_at)
SELECT
    p.user_id,
    i.playlist_id,
    i.library_item_id,
    i.episode_id,
    i.position,
    i.added_at
FROM abs_playlist_items i
JOIN abs_playlists p ON p.id = i.playlist_id
ON CONFLICT (user_id, collection_id, media_item_id, sub_item_id) DO NOTHING;

-- 6. Drop the abs_* collection tables. abs_playlist_items has a FK to
--    abs_playlists, so the order matters.
DROP TABLE abs_playlist_items;
DROP TABLE abs_playlists;
DROP TABLE abs_collection_items;
DROP TABLE abs_user_collections;
DROP TABLE abs_smart_collections;

-- 7. Media-type filter on page_sections. Default preserves current
--    behavior (existing rails surface movies+series only).
ALTER TABLE page_sections
    ADD COLUMN media_types text[] NOT NULL DEFAULT ARRAY['movie','series'];
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
