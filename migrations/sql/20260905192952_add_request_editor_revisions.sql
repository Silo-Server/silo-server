-- +goose Up
CREATE SEQUENCE request_editor_revision_seq;
ALTER TABLE request_settings ADD COLUMN revision bigint NOT NULL DEFAULT nextval('request_editor_revision_seq');
ALTER TABLE request_user_limits ADD COLUMN revision bigint NOT NULL DEFAULT nextval('request_editor_revision_seq');
ALTER TABLE request_integrations ADD COLUMN revision bigint NOT NULL DEFAULT nextval('request_editor_revision_seq');
-- +goose StatementBegin
CREATE FUNCTION advance_request_editor_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := nextval('request_editor_revision_seq');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER request_settings_revision BEFORE INSERT OR UPDATE ON request_settings FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();
CREATE TRIGGER request_user_limits_revision BEFORE INSERT OR UPDATE ON request_user_limits FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();
CREATE TRIGGER request_integrations_revision BEFORE INSERT OR UPDATE ON request_integrations FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();

-- +goose Down
DROP TRIGGER request_settings_revision ON request_settings;
DROP TRIGGER request_user_limits_revision ON request_user_limits;
DROP TRIGGER request_integrations_revision ON request_integrations;
DROP FUNCTION advance_request_editor_revision();
ALTER TABLE request_settings DROP COLUMN revision;
ALTER TABLE request_user_limits DROP COLUMN revision;
ALTER TABLE request_integrations DROP COLUMN revision;
DROP SEQUENCE request_editor_revision_seq;
