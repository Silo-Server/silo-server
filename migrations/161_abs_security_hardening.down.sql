DROP INDEX IF EXISTS abs_sessions_user_profile_idx;

-- Restore the pre-hardening key shape. Rows that differ only by sub_item_id
-- cannot be represented by the old key, so keep one representative row.
DELETE FROM user_personal_collection_items dup
USING user_personal_collection_items keep
WHERE dup.user_id = keep.user_id
  AND dup.collection_id = keep.collection_id
  AND dup.media_item_id = keep.media_item_id
  AND dup.ctid > keep.ctid;

ALTER TABLE user_personal_collection_items
    DROP CONSTRAINT IF EXISTS user_personal_collection_items_pkey;
ALTER TABLE user_personal_collection_items
    ADD CONSTRAINT user_personal_collection_items_pkey
    PRIMARY KEY (user_id, collection_id, media_item_id);

ALTER TABLE abs_sessions
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS token_type,
    DROP COLUMN IF EXISTS profile_id;
