-- +goose Up
CREATE SEQUENCE api_key_configuration_revision_seq AS bigint;
ALTER TABLE api_keys ADD COLUMN revision bigint NOT NULL DEFAULT nextval('api_key_configuration_revision_seq');

-- Configuration tags cover stable credential identity, not authentication
-- activity. Existing credentials and ownership remain unchanged. Replacement
-- uses delete/create, whose new generation cannot accept a stale editor tag.
-- +goose StatementBegin
CREATE FUNCTION advance_api_key_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('api_key_configuration_revision_seq');
    ELSE
        IF ROW(NEW.id,NEW.user_id,NEW.api_key) IS DISTINCT FROM ROW(OLD.id,OLD.user_id,OLD.api_key) THEN
            RAISE EXCEPTION 'API key identity, owner, and credential are immutable' USING ERRCODE='23514';
        END IF;
        IF ROW(NEW.label,NEW.rate_tier,COALESCE(NEW.scopes,'{}'::text[]),NEW.created_at)
           IS DISTINCT FROM ROW(OLD.label,OLD.rate_tier,COALESCE(OLD.scopes,'{}'::text[]),OLD.created_at) THEN
            NEW.revision := nextval('api_key_configuration_revision_seq');
        ELSE
            NEW.revision := OLD.revision;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER api_key_configuration_revision BEFORE INSERT OR UPDATE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION advance_api_key_configuration_revision();
ALTER TABLE api_keys ALTER COLUMN revision DROP DEFAULT;
CREATE INDEX api_keys_configuration_page ON api_keys(created_at DESC,id DESC);

-- +goose Down
DROP INDEX api_keys_configuration_page;
DROP TRIGGER api_key_configuration_revision ON api_keys;
DROP FUNCTION advance_api_key_configuration_revision();
ALTER TABLE api_keys DROP COLUMN revision;
DROP SEQUENCE api_key_configuration_revision_seq;
