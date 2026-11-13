-- Rewrite every stored playback.preferred_quality of "original" to "auto",
-- and the legacy user_profiles.quality_preference column with it.
--
-- Settings manifest revision 20 deprecates "original". Auto with no bitrate
-- cap already plays the source whenever the device can, and the planner now
-- treats "original" as auto, so the two choices only confused people. The
-- value stays valid so an older client can still write it, but current
-- clients no longer offer it and would show a stored "original" as a choice
-- their picker cannot display.
--
-- The profile column is no longer what playback resolves, but the profile API
-- still returns it, so it is rewritten too rather than left showing a value
-- the apps no longer offer.
--
-- The revision moves so a client holding the old row sees a conflict and
-- re-reads it. updated_at is left alone: the admin device surfaces sort and
-- label devices by it, and this is not a change anyone made to a device.

-- +goose Up
-- +goose StatementBegin
UPDATE public.user_setting_values
SET value = '"auto"'::jsonb,
    revision = revision + 1
WHERE key = 'playback.preferred_quality'
  AND value = '"original"'::jsonb;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE public.user_profiles
SET quality_preference = 'auto'
WHERE quality_preference = 'original';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Deliberately empty: nothing records which rows held "original", and auto
-- plays the same file the old value did.
SELECT 1;
-- +goose StatementEnd
