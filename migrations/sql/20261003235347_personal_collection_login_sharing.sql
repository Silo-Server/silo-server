-- +goose Up
-- +goose StatementBegin
-- #1615: a personal collection is either private to the profile that created it
-- or shared with every profile on its login. Visibility is decided at read time
-- from is_shared; the per-profile allow list and the login-wide collection
-- groups stop being read.

-- Audiobookshelf collections and playlists share this table but never had
-- visibility rows, and native routes must keep ignoring them. Native rows always
-- carry at least their creator's visibility row (written on create since
-- migration 016), so that row marks them. New native rows set the column
-- explicitly; the default keeps every other writer out of native listings.
ALTER TABLE user_personal_collections ADD COLUMN native BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE user_personal_collections c
   SET native = TRUE
 WHERE EXISTS (
         SELECT 1
           FROM user_personal_collection_profiles v
          WHERE v.user_id = c.user_id
            AND v.collection_id = c.id
       );

-- A collection stays shared only when its allow list covers every current
-- profile of its login (its creator implied). Shared with some profiles, or
-- with nobody but its creator, it becomes private, so no profile gains access
-- its owner withheld. Allow-list rows of deleted profiles do not count.
UPDATE user_personal_collections c
   SET is_shared = FALSE
 WHERE c.native
   AND c.is_shared
   AND EXISTS (
         SELECT 1
           FROM user_profiles p
          WHERE p.user_id = c.user_id
            AND p.id <> c.creator_profile_id
            AND NOT EXISTS (
                  SELECT 1
                    FROM user_personal_collection_profiles v
                   WHERE v.user_id = c.user_id
                     AND v.collection_id = c.id
                     AND v.profile_id = p.id
                )
       );

-- Each profile gets one flat order of its own collections, numbered from 0:
-- grouped collections first in group order (as the groups were listed:
-- sort_order, name, id), each group in its stored collection order, then the
-- ungrouped collections.
WITH ranked AS (
    SELECT c.user_id,
           c.id,
           ROW_NUMBER() OVER (
               PARTITION BY c.user_id, c.creator_profile_id
               ORDER BY (c.group_id IS NULL), g.sort_order, g.name, g.id,
                        c.sort_order, c.created_at, c.id
           ) - 1 AS pos
      FROM user_personal_collections c
      LEFT JOIN user_collection_groups g
             ON g.user_id = c.user_id
            AND g.id = c.group_id
     WHERE c.native
)
UPDATE user_personal_collections c
   SET sort_order = r.pos,
       group_id = NULL
  FROM ranked r
 WHERE c.user_id = r.user_id
   AND c.id = r.id
   AND (c.sort_order <> r.pos OR c.group_id IS NOT NULL);

-- Personal collection groups are gone. The table stays (empty) until the
-- retired sharing and group tables are dropped.
DELETE FROM user_collection_groups;

CREATE INDEX idx_user_personal_collections_creator_order
    ON user_personal_collections (user_id, creator_profile_id, sort_order);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The previous code reads visibility from the allow list alone, so rewrite it
-- from is_shared: the creator, plus every profile of the login when shared.
-- Groups and the previous order are not restored; restore a backup for those.
DELETE FROM user_personal_collection_profiles v
 USING user_personal_collections c
 WHERE c.user_id = v.user_id
   AND c.id = v.collection_id
   AND c.native;

INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id)
SELECT c.user_id, c.id, c.creator_profile_id
  FROM user_personal_collections c
 WHERE c.native
UNION
SELECT c.user_id, c.id, p.id
  FROM user_personal_collections c
  JOIN user_profiles p ON p.user_id = c.user_id
 WHERE c.native
   AND c.is_shared;

DROP INDEX IF EXISTS idx_user_personal_collections_creator_order;
ALTER TABLE user_personal_collections DROP COLUMN IF EXISTS native;
-- +goose StatementEnd
