-- +goose Up
-- +goose StatementBegin
CREATE SEQUENCE access_group_configuration_revision_seq;
ALTER TABLE access_groups ADD COLUMN configuration_revision bigint NOT NULL DEFAULT nextval('access_group_configuration_revision_seq');
CREATE FUNCTION advance_access_group_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER access_group_configuration_revision BEFORE INSERT OR UPDATE ON access_groups FOR EACH ROW EXECUTE FUNCTION advance_access_group_configuration_revision();
ALTER TABLE access_groups ALTER COLUMN configuration_revision DROP DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER access_group_configuration_revision ON access_groups;
DROP FUNCTION advance_access_group_configuration_revision();
ALTER TABLE access_groups DROP COLUMN configuration_revision;
DROP SEQUENCE access_group_configuration_revision_seq;
-- +goose StatementEnd
