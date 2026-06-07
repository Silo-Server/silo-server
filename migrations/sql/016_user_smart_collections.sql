-- +goose Up
-- +goose StatementBegin
ALTER TABLE user_personal_collections
    ADD COLUMN creator_profile_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN collection_mode TEXT NOT NULL DEFAULT 'manual',
    ADD COLUMN is_shared BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN query_definition JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN sort_config JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE user_personal_collections
SET creator_profile_id = profile_id
WHERE creator_profile_id = '';

CREATE TABLE user_personal_collection_profiles (
    user_id INTEGER NOT NULL,
    collection_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    PRIMARY KEY (user_id, collection_id, profile_id),
    CONSTRAINT user_personal_collection_profiles_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id)
SELECT user_id, id, profile_id
FROM user_personal_collections
ON CONFLICT (user_id, collection_id, profile_id) DO NOTHING;

CREATE INDEX idx_user_collection_profiles_lookup
    ON user_personal_collection_profiles (user_id, profile_id, collection_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE user_personal_collections upc
SET profile_id = ucpp.profile_id
FROM (
    SELECT DISTINCT ON (user_id, collection_id) user_id, collection_id, profile_id
    FROM user_personal_collection_profiles
    ORDER BY user_id, collection_id, profile_id
) ucpp
WHERE upc.user_id = ucpp.user_id AND upc.id = ucpp.collection_id;

DROP INDEX IF EXISTS idx_user_collection_profiles_lookup;
DROP TABLE IF EXISTS user_personal_collection_profiles;

ALTER TABLE user_personal_collections
    DROP COLUMN IF EXISTS sort_config,
    DROP COLUMN IF EXISTS query_definition,
    DROP COLUMN IF EXISTS is_shared,
    DROP COLUMN IF EXISTS collection_mode,
    DROP COLUMN IF EXISTS creator_profile_id;
-- +goose StatementEnd
