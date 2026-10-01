-- +goose Up
CREATE SEQUENCE history_import_editor_revision_seq;
ALTER TABLE history_import_sources ADD COLUMN revision bigint NOT NULL DEFAULT nextval('history_import_editor_revision_seq');
ALTER TABLE history_import_user_mappings ADD COLUMN revision bigint NOT NULL DEFAULT nextval('history_import_editor_revision_seq');
-- +goose StatementBegin
CREATE FUNCTION advance_history_import_source_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('history_import_editor_revision_seq');
    ELSIF ROW(NEW.id, NEW.name, NEW.source_type, NEW.base_url, NEW.system_id, NEW.enabled, NEW.sort_order, NEW.admin_token)
       IS DISTINCT FROM ROW(OLD.id, OLD.name, OLD.source_type, OLD.base_url, OLD.system_id, OLD.enabled, OLD.sort_order, OLD.admin_token) THEN
        NEW.revision := nextval('history_import_editor_revision_seq');
    ELSE
        NEW.revision := OLD.revision;
    END IF;
    RETURN NEW;
END;
$$;
CREATE FUNCTION advance_history_import_mapping_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('history_import_editor_revision_seq');
    ELSIF ROW(NEW.id, NEW.source_id, NEW.external_user_id, NEW.external_user_name, NEW.silo_user_id, NEW.silo_profile_id)
       IS DISTINCT FROM ROW(OLD.id, OLD.source_id, OLD.external_user_id, OLD.external_user_name, OLD.silo_user_id, OLD.silo_profile_id) THEN
        NEW.revision := nextval('history_import_editor_revision_seq');
    ELSE
        NEW.revision := OLD.revision;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER history_import_source_revision BEFORE INSERT OR UPDATE ON history_import_sources FOR EACH ROW EXECUTE FUNCTION advance_history_import_source_revision();
CREATE TRIGGER history_import_mapping_revision BEFORE INSERT OR UPDATE ON history_import_user_mappings FOR EACH ROW EXECUTE FUNCTION advance_history_import_mapping_revision();

-- +goose Down
DROP TRIGGER history_import_source_revision ON history_import_sources;
DROP TRIGGER history_import_mapping_revision ON history_import_user_mappings;
DROP FUNCTION advance_history_import_source_revision();
DROP FUNCTION advance_history_import_mapping_revision();
ALTER TABLE history_import_sources DROP COLUMN revision;
ALTER TABLE history_import_user_mappings DROP COLUMN revision;
DROP SEQUENCE history_import_editor_revision_seq;
