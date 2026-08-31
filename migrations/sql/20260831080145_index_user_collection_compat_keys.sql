-- +goose Up
-- Explicit UTF-8 makes the lookup key independent of client encoding. Keep
-- the 112-bit SHA-256 prefix in sync with ResourceIDCodec's personal ID kind.
-- +goose StatementBegin
CREATE FUNCTION jellycompat_user_collection_key(collection_id text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT substr(encode(sha256(convert_to(collection_id, 'UTF8')), 'hex'), 1, 28)
$$;
-- +goose StatementEnd

CREATE INDEX idx_user_personal_collections_compat_key
    ON user_personal_collections (jellycompat_user_collection_key(id))
    WHERE include_in_server_collections = TRUE
      AND collection_type <> 'playlist';

DROP INDEX idx_user_personal_collections_image_candidates;

-- +goose Down
CREATE INDEX idx_user_personal_collections_image_candidates
    ON user_personal_collections (id)
    WHERE include_in_server_collections = TRUE
      AND collection_type <> 'playlist';

DROP INDEX idx_user_personal_collections_compat_key;
DROP FUNCTION jellycompat_user_collection_key(text);
