-- Persist ABS token scope and preserve playlist episode granularity for DBs
-- that already applied migration 156 before sub_item_id joined the PK.

ALTER TABLE abs_sessions
    ADD COLUMN IF NOT EXISTS profile_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS token_type text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS expires_at timestamptz;

CREATE INDEX IF NOT EXISTS abs_sessions_user_profile_idx
    ON abs_sessions (user_id, profile_id)
    WHERE revoked_at IS NULL;

ALTER TABLE user_personal_collection_items
    DROP CONSTRAINT IF EXISTS user_personal_collection_items_pkey;
ALTER TABLE user_personal_collection_items
    ADD CONSTRAINT user_personal_collection_items_pkey
    PRIMARY KEY (user_id, collection_id, media_item_id, sub_item_id);
