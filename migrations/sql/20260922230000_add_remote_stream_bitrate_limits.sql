-- +goose Up
ALTER TABLE access_groups
    ADD COLUMN max_remote_stream_bitrate_kbps integer NOT NULL DEFAULT 0
        CHECK (max_remote_stream_bitrate_kbps >= 0);

ALTER TABLE users
    ADD COLUMN max_remote_stream_bitrate_kbps integer
        CHECK (max_remote_stream_bitrate_kbps >= 0);

ALTER TABLE playback_v3_attempts
    ADD COLUMN server_bitrate_cap_kbps integer NOT NULL DEFAULT 0;

-- Keep access-group ETags accurate even when a policy is changed outside the admin API.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_access_group_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSIF ROW(NEW.id,NEW.name,NEW.description,NEW.library_ids,NEW.max_playback_quality,
 NEW.download_allowed,NEW.download_transcode_allowed,NEW.transcode_allowed,NEW.audio_transcode_allowed,
 NEW.max_streams,NEW.max_transcodes,NEW.max_remote_stream_bitrate_kbps,NEW.allowed_permissions,NEW.requests_allowed,NEW.is_default,NEW.created_at,NEW.updated_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.name,OLD.description,OLD.library_ids,OLD.max_playback_quality,
 OLD.download_allowed,OLD.download_transcode_allowed,OLD.transcode_allowed,OLD.audio_transcode_allowed,
 OLD.max_streams,OLD.max_transcodes,OLD.max_remote_stream_bitrate_kbps,OLD.allowed_permissions,OLD.requests_allowed,OLD.is_default,OLD.created_at,OLD.updated_at) THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSE
  NEW.configuration_revision := OLD.configuration_revision;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_access_group_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSIF ROW(NEW.id,NEW.name,NEW.description,NEW.library_ids,NEW.max_playback_quality,
 NEW.download_allowed,NEW.download_transcode_allowed,NEW.transcode_allowed,NEW.audio_transcode_allowed,
 NEW.max_streams,NEW.max_transcodes,NEW.allowed_permissions,NEW.requests_allowed,NEW.is_default,NEW.created_at,NEW.updated_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.name,OLD.description,OLD.library_ids,OLD.max_playback_quality,
 OLD.download_allowed,OLD.download_transcode_allowed,OLD.transcode_allowed,OLD.audio_transcode_allowed,
 OLD.max_streams,OLD.max_transcodes,OLD.allowed_permissions,OLD.requests_allowed,OLD.is_default,OLD.created_at,OLD.updated_at) THEN
  NEW.configuration_revision := nextval('access_group_configuration_revision_seq');
 ELSE
  NEW.configuration_revision := OLD.configuration_revision;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts DROP COLUMN server_bitrate_cap_kbps;
ALTER TABLE users DROP COLUMN max_remote_stream_bitrate_kbps;
ALTER TABLE access_groups DROP COLUMN max_remote_stream_bitrate_kbps;
