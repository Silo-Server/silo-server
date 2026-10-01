-- +goose Up
CREATE TABLE user_collection_order_revisions (
 user_id INTEGER PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 1
);
INSERT INTO user_collection_order_revisions(user_id) SELECT id FROM users;
-- +goose StatementBegin
CREATE FUNCTION user_personal_collections_order_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM new_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collections_order_revision_insert AFTER INSERT ON user_personal_collections
 REFERENCING NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collections_order_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collections_order_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM old_rows UNION SELECT user_id FROM new_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collections_order_revision_update AFTER UPDATE ON user_personal_collections
 REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collections_order_revision_update();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collections_order_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM old_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collections_order_revision_delete AFTER DELETE ON user_personal_collections
 REFERENCING OLD TABLE AS old_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collections_order_revision_delete();
-- +goose StatementBegin
CREATE FUNCTION user_collection_groups_order_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM new_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_collection_groups_order_revision_insert AFTER INSERT ON user_collection_groups
 REFERENCING NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_collection_groups_order_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION user_collection_groups_order_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM old_rows UNION SELECT user_id FROM new_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_collection_groups_order_revision_update AFTER UPDATE ON user_collection_groups
 REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_collection_groups_order_revision_update();
-- +goose StatementBegin
CREATE FUNCTION user_collection_groups_order_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM old_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_collection_groups_order_revision_delete AFTER DELETE ON user_collection_groups
 REFERENCING OLD TABLE AS old_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_collection_groups_order_revision_delete();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_profiles_order_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM new_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_profiles_order_revision_insert AFTER INSERT ON user_personal_collection_profiles
 REFERENCING NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collection_profiles_order_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_profiles_order_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM old_rows UNION SELECT user_id FROM new_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_profiles_order_revision_update AFTER UPDATE ON user_personal_collection_profiles
 REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collection_profiles_order_revision_update();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_profiles_order_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO user_collection_order_revisions(user_id,revision)
 SELECT user_id,2 FROM (SELECT user_id FROM old_rows) affected GROUP BY user_id ORDER BY user_id
 ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_profiles_order_revision_delete AFTER DELETE ON user_personal_collection_profiles
 REFERENCING OLD TABLE AS old_rows
 FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collection_profiles_order_revision_delete();

-- +goose Down
DROP TRIGGER user_personal_collections_order_revision_insert ON user_personal_collections;
DROP FUNCTION user_personal_collections_order_revision_insert();
DROP TRIGGER user_personal_collections_order_revision_update ON user_personal_collections;
DROP FUNCTION user_personal_collections_order_revision_update();
DROP TRIGGER user_personal_collections_order_revision_delete ON user_personal_collections;
DROP FUNCTION user_personal_collections_order_revision_delete();
DROP TRIGGER user_collection_groups_order_revision_insert ON user_collection_groups;
DROP FUNCTION user_collection_groups_order_revision_insert();
DROP TRIGGER user_collection_groups_order_revision_update ON user_collection_groups;
DROP FUNCTION user_collection_groups_order_revision_update();
DROP TRIGGER user_collection_groups_order_revision_delete ON user_collection_groups;
DROP FUNCTION user_collection_groups_order_revision_delete();
DROP TRIGGER user_personal_collection_profiles_order_revision_insert ON user_personal_collection_profiles;
DROP FUNCTION user_personal_collection_profiles_order_revision_insert();
DROP TRIGGER user_personal_collection_profiles_order_revision_update ON user_personal_collection_profiles;
DROP FUNCTION user_personal_collection_profiles_order_revision_update();
DROP TRIGGER user_personal_collection_profiles_order_revision_delete ON user_personal_collection_profiles;
DROP FUNCTION user_personal_collection_profiles_order_revision_delete();
DROP TABLE user_collection_order_revisions;
