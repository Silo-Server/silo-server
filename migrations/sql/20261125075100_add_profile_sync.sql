-- +goose Up
-- The profile set: an account's household profiles kept the same across the
-- servers one person uses.
--
-- users.profile_set_*: the version and content hash (SHA-256, 32 bytes) of the
-- last set the account accepted, and when it accepted a newer one. All three
-- are NULL on an account that never took a set.
ALTER TABLE public.users
    ADD COLUMN profile_set_version     bigint,
    ADD COLUMN profile_set_sha256      bytea,
    ADD COLUMN profile_set_accepted_at timestamptz,
    ADD CONSTRAINT users_profile_set_version_range
        CHECK (profile_set_version IS NULL OR profile_set_version BETWEEN 0 AND 9007199254740991),
    ADD CONSTRAINT users_profile_set_sha256_length
        CHECK (profile_set_sha256 IS NULL OR length(profile_set_sha256) = 32),
    ADD CONSTRAINT users_profile_set_state_together CHECK (
        (profile_set_version IS NULL) = (profile_set_sha256 IS NULL)
        AND (profile_set_version IS NULL) = (profile_set_accepted_at IS NULL));

-- user_profiles.sync_id: the profile's ID in the set, the same on every server
-- (NULL for a profile that is not in one). sync_position is its place in the
-- set's order. sync_applied holds the values the set last wrote to the
-- profile, per field. avatar_source_sha256 is the SHA-256 of the uploaded
-- picture's file as it was first uploaded, lower-case hex; it is NULL for a
-- preset or no picture.
ALTER TABLE public.user_profiles
    ADD COLUMN sync_id              uuid,
    ADD COLUMN sync_position        integer,
    ADD COLUMN sync_applied         jsonb,
    ADD COLUMN avatar_source_sha256 text,
    ADD CONSTRAINT user_profiles_sync_position_nonnegative
        CHECK (sync_position IS NULL OR sync_position >= 0),
    ADD CONSTRAINT user_profiles_sync_fields_together CHECK (
        (sync_id IS NULL) = (sync_position IS NULL)
        AND (sync_id IS NOT NULL OR sync_applied IS NULL)),
    ADD CONSTRAINT user_profiles_avatar_source_sha256_form
        CHECK (avatar_source_sha256 IS NULL OR avatar_source_sha256 ~ '^[0-9a-f]{64}$');

-- Unique per account, not per server: one person may hold two accounts on one
-- server, each with the same set.
CREATE UNIQUE INDEX user_profiles_sync_id_per_user
    ON public.user_profiles (user_id, sync_id) WHERE sync_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS public.user_profiles_sync_id_per_user;
ALTER TABLE public.user_profiles
    DROP CONSTRAINT IF EXISTS user_profiles_avatar_source_sha256_form,
    DROP CONSTRAINT IF EXISTS user_profiles_sync_fields_together,
    DROP CONSTRAINT IF EXISTS user_profiles_sync_position_nonnegative,
    DROP COLUMN IF EXISTS avatar_source_sha256,
    DROP COLUMN IF EXISTS sync_applied,
    DROP COLUMN IF EXISTS sync_position,
    DROP COLUMN IF EXISTS sync_id;
ALTER TABLE public.users
    DROP CONSTRAINT IF EXISTS users_profile_set_state_together,
    DROP CONSTRAINT IF EXISTS users_profile_set_sha256_length,
    DROP CONSTRAINT IF EXISTS users_profile_set_version_range,
    DROP COLUMN IF EXISTS profile_set_accepted_at,
    DROP COLUMN IF EXISTS profile_set_sha256,
    DROP COLUMN IF EXISTS profile_set_version;
