-- Drop user_devices' FK to public.user_profiles.
--
-- Profiles are not guaranteed to live in Postgres: with the sqlite userdb
-- backend they exist only in per-user SQLite stores and public.user_profiles
-- stays empty, so any insert into user_devices (managed downloads,
-- subscriptions, offline sync) violated this constraint and 500'd. Shared
-- Postgres tables must not FK profile tables (same rule as the notifications
-- tables); profile-scoped cleanup happens in application code instead
-- (see ProfileHandler's device-library purge).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_devices DROP CONSTRAINT IF EXISTS user_devices_profile_fkey;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- NOT VALID: rolling back on a sqlite-backend deployment must not fail on
-- rows whose profiles never existed in public.user_profiles.
ALTER TABLE public.user_devices ADD CONSTRAINT user_devices_profile_fkey
    FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id)
    ON DELETE CASCADE NOT VALID;
-- +goose StatementEnd
