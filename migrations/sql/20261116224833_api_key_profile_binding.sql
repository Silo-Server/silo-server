-- Bind an API key to one household profile.
--
-- NULL keeps today's behavior: the key is an account credential that may name
-- any profile on its account. A bound key always acts as its profile, so it
-- carries that profile's limits and cannot name another. Deleting the profile
-- deletes its keys. A bound key is always an allowlist credential: it must
-- carry scopes.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN profile_id text;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_profile_fkey
    FOREIGN KEY (user_id, profile_id) REFERENCES user_profiles(user_id, id) ON DELETE CASCADE;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_profile_scoped
    CHECK (profile_id IS NULL OR cardinality(scopes) > 0);
CREATE INDEX idx_api_keys_profile ON api_keys(user_id, profile_id) WHERE profile_id IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_api_key_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('api_key_configuration_revision_seq');
    ELSE
        IF ROW(NEW.id,NEW.user_id,NEW.key_hash,NEW.key_prefix,NEW.profile_id)
           IS DISTINCT FROM ROW(OLD.id,OLD.user_id,OLD.key_hash,OLD.key_prefix,OLD.profile_id) THEN
            RAISE EXCEPTION 'API key identity, owner, profile, and credential are immutable' USING ERRCODE='23514';
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

-- +goose Down
-- A bound key without its binding would act as the whole account.
DELETE FROM api_keys WHERE profile_id IS NOT NULL;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_api_key_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('api_key_configuration_revision_seq');
    ELSE
        IF ROW(NEW.id,NEW.user_id,NEW.key_hash,NEW.key_prefix) IS DISTINCT FROM ROW(OLD.id,OLD.user_id,OLD.key_hash,OLD.key_prefix) THEN
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
DROP INDEX idx_api_keys_profile;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_profile_scoped;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_profile_fkey;
ALTER TABLE api_keys DROP COLUMN profile_id;
