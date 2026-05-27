-- Unify user-owned lists into user_personal_collections.
--
-- See docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md
-- for design rationale.

-- 1. Sub-item granularity column on the canonical items table.
--    Empty string for whole-item entries; populated for podcast-episode
--    playlist entries (sub_item_id == abs_playlist_items.episode_id).
ALTER TABLE user_personal_collection_items
    ADD COLUMN sub_item_id text NOT NULL DEFAULT '';

-- 2. Widen the collection_type domain. The pre-existing CHECK (if any)
--    only admits 'manual' and 'synced'.
ALTER TABLE user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
    ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'synced', 'playlist', 'smart'));

-- 3. Move the existing abs_playlists row(s) into the canonical store.
--    is_public maps to is_shared; profile_id (uuid) is stringified.
INSERT INTO user_personal_collections
    (id, user_id, profile_id, name, description, collection_type,
     is_shared, created_at, updated_at, creator_profile_id)
SELECT
    id,
    user_id,
    COALESCE(profile_id::text, ''),
    name,
    description,
    'playlist',
    is_public,
    created_at,
    updated_at,
    COALESCE(profile_id::text, '')
FROM abs_playlists;

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
JOIN abs_playlists p ON p.id = i.playlist_id;

-- 4. Drop the abs_* collection tables. abs_playlist_items has a FK to
--    abs_playlists, so the order matters.
DROP TABLE abs_playlist_items;
DROP TABLE abs_playlists;
DROP TABLE abs_collection_items;
DROP TABLE abs_user_collections;
DROP TABLE abs_smart_collections;

-- 5. Media-type filter on page_sections. Default preserves current
--    behavior (existing rails surface movies+series only).
ALTER TABLE page_sections
    ADD COLUMN media_types text[] NOT NULL DEFAULT ARRAY['movie','series'];
