-- +goose Up
-- A table-wide, non-cycling sequence prevents old validators from matching a
-- deleted/recreated provider. Existing credentials and their AAD stay unchanged.
CREATE SEQUENCE subtitle_provider_config_revision_seq AS BIGINT NO CYCLE;
ALTER TABLE subtitle_provider_config ADD COLUMN revision BIGINT;
UPDATE subtitle_provider_config SET revision = nextval('subtitle_provider_config_revision_seq');
ALTER TABLE subtitle_provider_config
    ALTER COLUMN revision SET NOT NULL,
    ADD CONSTRAINT subtitle_provider_config_revision_positive CHECK (revision > 0);
ALTER SEQUENCE subtitle_provider_config_revision_seq OWNED BY subtitle_provider_config.revision;

-- Every writer gets a new revision, including the bridge and explicit SQL.
-- Sequence gaps are expected after failed/rolled-back writes.
-- +goose StatementBegin
CREATE FUNCTION assign_subtitle_provider_config_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := nextval('subtitle_provider_config_revision_seq');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER subtitle_provider_config_revision
    BEFORE INSERT OR UPDATE ON subtitle_provider_config
    FOR EACH ROW EXECUTE FUNCTION assign_subtitle_provider_config_revision();

-- +goose Down
DROP TRIGGER subtitle_provider_config_revision ON subtitle_provider_config;
DROP FUNCTION assign_subtitle_provider_config_revision();
-- Dropping the owning column also drops the revision sequence.
ALTER TABLE subtitle_provider_config DROP COLUMN revision;
