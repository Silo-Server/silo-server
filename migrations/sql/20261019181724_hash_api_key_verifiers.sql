-- +goose Up
-- Stop every old API replica before migrating: old binaries require the raw
-- api_key column. This transaction retains credentials and configuration tags.
ALTER TABLE api_keys ADD COLUMN key_prefix text NOT NULL DEFAULT '';
ALTER TABLE api_keys DISABLE TRIGGER api_key_configuration_revision;
-- A legacy value can equal another row's final digest. Restore uniqueness
-- after every row is converted, under this transaction's exclusive table lock.
ALTER TABLE api_keys DROP CONSTRAINT api_keys_api_key_key;
UPDATE api_keys SET
    key_prefix = CASE WHEN api_key ~ '^sa_[0-9a-f]{64}$' THEN left(api_key,11) ELSE '' END,
    api_key = encode(sha256(convert_to(api_key,'UTF8')),'hex');
ALTER TABLE api_keys RENAME COLUMN api_key TO key_hash;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_api_key_key UNIQUE (key_hash);
ALTER TABLE api_keys ADD CONSTRAINT api_keys_key_hash_format CHECK (key_hash ~ '^[0-9a-f]{64}$');
ALTER TABLE api_keys ADD CONSTRAINT api_keys_key_prefix_format CHECK (key_prefix ~ '^(sa_[0-9a-f]{8})?$');

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
ALTER TABLE api_keys ENABLE TRIGGER api_key_configuration_revision;

-- +goose Down
-- A verifier cannot recover a bearer. Only an empty table can be reverted.
-- Lock first so the check also sees keys that in-flight writers commit.
LOCK TABLE api_keys IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM api_keys) THEN
        RAISE EXCEPTION 'API key verifiers cannot be reversed while keys exist; use verifier-aware server code';
    END IF;
END;
$$;
-- +goose StatementEnd
ALTER TABLE api_keys DROP CONSTRAINT api_keys_key_hash_format;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_key_prefix_format;
ALTER TABLE api_keys RENAME COLUMN key_hash TO api_key;
ALTER TABLE api_keys DROP COLUMN key_prefix;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_api_key_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
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
