-- +goose Up
-- Access groups set the household profile limit, and an account's
-- max_profiles becomes an override like its other policy fields: NULL
-- inherits the group's value. Every account stored a value, 5 unless an admin
-- changed it (093), so a 5 becomes NULL and inherits the new group column's 5;
-- any other value was chosen by an admin and stays as an override. Accounts
-- outside every group, admins included, resolve NULL to 5
-- (access.DefaultMaxProfiles), so no account's limit changes here.
ALTER TABLE access_groups
    ADD COLUMN max_profiles integer NOT NULL DEFAULT 5
        CONSTRAINT access_groups_max_profiles_min_check CHECK (max_profiles >= 1);

ALTER TABLE users
    ALTER COLUMN max_profiles DROP NOT NULL,
    ALTER COLUMN max_profiles DROP DEFAULT;

UPDATE users SET max_profiles = NULL WHERE max_profiles = 5;

-- Keep access-group ETags accurate when the profile limit changes.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_access_group_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSIF ROW(NEW.id,NEW.name,NEW.description,NEW.library_ids,NEW.max_playback_quality,
 NEW.download_allowed,NEW.download_transcode_allowed,NEW.transcode_allowed,NEW.audio_transcode_allowed,
 NEW.max_streams,NEW.max_transcodes,NEW.max_remote_stream_bitrate_kbps,NEW.max_local_stream_bitrate_kbps,
 NEW.max_profiles,NEW.allowed_permissions,NEW.requests_allowed,NEW.is_default,NEW.created_at,NEW.updated_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.name,OLD.description,OLD.library_ids,OLD.max_playback_quality,
 OLD.download_allowed,OLD.download_transcode_allowed,OLD.transcode_allowed,OLD.audio_transcode_allowed,
 OLD.max_streams,OLD.max_transcodes,OLD.max_remote_stream_bitrate_kbps,OLD.max_local_stream_bitrate_kbps,
 OLD.max_profiles,OLD.allowed_permissions,OLD.requests_allowed,OLD.is_default,OLD.created_at,OLD.updated_at) THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSE
  NEW.configuration_revision := OLD.configuration_revision;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Every account gets back a stored limit: the one it resolved to, so an
-- account that inherited a group's limit keeps it on the previous version,
-- which ignores groups. Lossy by construction: the old column can't say
-- "inherit", so running Up again turns only the 5s back into inherit and
-- keeps any other group limit as an override.
UPDATE users u
SET max_profiles = COALESCE(
    (SELECT g.max_profiles FROM access_groups g WHERE g.id = u.access_group_id AND u.role <> 'admin'),
    5)
WHERE u.max_profiles IS NULL;

ALTER TABLE users
    ALTER COLUMN max_profiles SET DEFAULT 5,
    ALTER COLUMN max_profiles SET NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_access_group_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSIF ROW(NEW.id,NEW.name,NEW.description,NEW.library_ids,NEW.max_playback_quality,
 NEW.download_allowed,NEW.download_transcode_allowed,NEW.transcode_allowed,NEW.audio_transcode_allowed,
 NEW.max_streams,NEW.max_transcodes,NEW.max_remote_stream_bitrate_kbps,NEW.max_local_stream_bitrate_kbps,
 NEW.allowed_permissions,NEW.requests_allowed,NEW.is_default,NEW.created_at,NEW.updated_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.name,OLD.description,OLD.library_ids,OLD.max_playback_quality,
 OLD.download_allowed,OLD.download_transcode_allowed,OLD.transcode_allowed,OLD.audio_transcode_allowed,
 OLD.max_streams,OLD.max_transcodes,OLD.max_remote_stream_bitrate_kbps,OLD.max_local_stream_bitrate_kbps,
 OLD.allowed_permissions,OLD.requests_allowed,OLD.is_default,OLD.created_at,OLD.updated_at) THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSE
  NEW.configuration_revision := OLD.configuration_revision;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

ALTER TABLE access_groups DROP COLUMN max_profiles;
