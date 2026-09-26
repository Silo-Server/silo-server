-- +goose Up
-- Persisted membership witnesses work across API nodes and cover every writer.
CREATE TABLE library_collection_revisions (
    collection_id TEXT PRIMARY KEY,
    revision BIGINT NOT NULL DEFAULT 1
);
INSERT INTO library_collection_revisions SELECT id, 1 FROM library_collections;
CREATE TABLE user_collection_revisions (
    user_id INTEGER NOT NULL,
    collection_id TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (user_id, collection_id)
);
INSERT INTO user_collection_revisions SELECT user_id, id, 1 FROM user_personal_collections;

-- +goose StatementBegin
CREATE FUNCTION library_collections_advance_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        INSERT INTO library_collection_revisions (collection_id, revision) VALUES (OLD.id, 1) ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        INSERT INTO library_collection_revisions (collection_id, revision) VALUES (NEW.id, 1) ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collections_advance_revision AFTER INSERT OR UPDATE OR DELETE ON library_collections
    FOR EACH ROW EXECUTE FUNCTION library_collections_advance_revision();

-- +goose StatementBegin
CREATE FUNCTION library_collection_items_advance_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO library_collection_revisions (collection_id, revision)
    SELECT collection_id, 1 FROM (SELECT collection_id FROM new_rows) affected
    GROUP BY collection_id ORDER BY collection_id
    ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_items_advance_revision_insert AFTER INSERT ON library_collection_items
    REFERENCING NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION library_collection_items_advance_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION library_collection_items_advance_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO library_collection_revisions (collection_id, revision)
    SELECT collection_id, 1 FROM (SELECT collection_id FROM old_rows UNION SELECT collection_id FROM new_rows) affected
    GROUP BY collection_id ORDER BY collection_id
    ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_items_advance_revision_update AFTER UPDATE ON library_collection_items
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION library_collection_items_advance_revision_update();
-- +goose StatementBegin
CREATE FUNCTION library_collection_items_advance_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO library_collection_revisions (collection_id, revision)
    SELECT collection_id, 1 FROM (SELECT collection_id FROM old_rows) affected
    GROUP BY collection_id ORDER BY collection_id
    ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_items_advance_revision_delete AFTER DELETE ON library_collection_items
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT EXECUTE FUNCTION library_collection_items_advance_revision_delete();

-- +goose StatementBegin
CREATE FUNCTION library_collection_libraries_advance_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        INSERT INTO library_collection_revisions (collection_id, revision) VALUES (OLD.collection_id, 1) ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        INSERT INTO library_collection_revisions (collection_id, revision) VALUES (NEW.collection_id, 1) ON CONFLICT (collection_id) DO UPDATE SET revision = library_collection_revisions.revision + 1;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_libraries_advance_revision AFTER INSERT OR UPDATE OR DELETE ON library_collection_libraries
    FOR EACH ROW EXECUTE FUNCTION library_collection_libraries_advance_revision();

-- +goose StatementBegin
CREATE FUNCTION user_personal_collections_advance_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        INSERT INTO user_collection_revisions (user_id, collection_id, revision) VALUES (OLD.user_id, OLD.id, 1) ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        INSERT INTO user_collection_revisions (user_id, collection_id, revision) VALUES (NEW.user_id, NEW.id, 1) ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collections_advance_revision AFTER INSERT OR UPDATE OR DELETE ON user_personal_collections
    FOR EACH ROW EXECUTE FUNCTION user_personal_collections_advance_revision();

-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_items_advance_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO user_collection_revisions (user_id, collection_id, revision)
    SELECT user_id, collection_id, 1 FROM (SELECT user_id, collection_id FROM new_rows) affected
    GROUP BY user_id, collection_id ORDER BY user_id, collection_id
    ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_items_advance_revision_insert AFTER INSERT ON user_personal_collection_items
    REFERENCING NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collection_items_advance_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_items_advance_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO user_collection_revisions (user_id, collection_id, revision)
    SELECT user_id, collection_id, 1 FROM (SELECT user_id, collection_id FROM old_rows UNION SELECT user_id, collection_id FROM new_rows) affected
    GROUP BY user_id, collection_id ORDER BY user_id, collection_id
    ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_items_advance_revision_update AFTER UPDATE ON user_personal_collection_items
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collection_items_advance_revision_update();
-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_items_advance_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO user_collection_revisions (user_id, collection_id, revision)
    SELECT user_id, collection_id, 1 FROM (SELECT user_id, collection_id FROM old_rows) affected
    GROUP BY user_id, collection_id ORDER BY user_id, collection_id
    ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_items_advance_revision_delete AFTER DELETE ON user_personal_collection_items
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT EXECUTE FUNCTION user_personal_collection_items_advance_revision_delete();

-- +goose StatementBegin
CREATE FUNCTION user_personal_collection_profiles_advance_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        INSERT INTO user_collection_revisions (user_id, collection_id, revision) VALUES (OLD.user_id, OLD.collection_id, 1) ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        INSERT INTO user_collection_revisions (user_id, collection_id, revision) VALUES (NEW.user_id, NEW.collection_id, 1) ON CONFLICT (user_id, collection_id) DO UPDATE SET revision = user_collection_revisions.revision + 1;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER user_personal_collection_profiles_advance_revision AFTER INSERT OR UPDATE OR DELETE ON user_personal_collection_profiles
    FOR EACH ROW EXECUTE FUNCTION user_personal_collection_profiles_advance_revision();

CREATE INDEX library_collection_items_continuation_idx ON library_collection_items (collection_id, (COALESCE(position, 0)), media_item_id);
CREATE INDEX user_collection_items_continuation_idx ON user_personal_collection_items (user_id, collection_id, (COALESCE(position, 0)), media_item_id) WHERE sub_item_id = '';

-- +goose Down
DROP INDEX library_collection_items_continuation_idx;
DROP INDEX user_collection_items_continuation_idx;
DROP TRIGGER library_collections_advance_revision ON library_collections;
DROP FUNCTION library_collections_advance_revision();
DROP TRIGGER library_collection_items_advance_revision_insert ON library_collection_items;
DROP FUNCTION library_collection_items_advance_revision_insert();
DROP TRIGGER library_collection_items_advance_revision_update ON library_collection_items;
DROP FUNCTION library_collection_items_advance_revision_update();
DROP TRIGGER library_collection_items_advance_revision_delete ON library_collection_items;
DROP FUNCTION library_collection_items_advance_revision_delete();
DROP TRIGGER library_collection_libraries_advance_revision ON library_collection_libraries;
DROP FUNCTION library_collection_libraries_advance_revision();
DROP TRIGGER user_personal_collections_advance_revision ON user_personal_collections;
DROP FUNCTION user_personal_collections_advance_revision();
DROP TRIGGER user_personal_collection_items_advance_revision_insert ON user_personal_collection_items;
DROP FUNCTION user_personal_collection_items_advance_revision_insert();
DROP TRIGGER user_personal_collection_items_advance_revision_update ON user_personal_collection_items;
DROP FUNCTION user_personal_collection_items_advance_revision_update();
DROP TRIGGER user_personal_collection_items_advance_revision_delete ON user_personal_collection_items;
DROP FUNCTION user_personal_collection_items_advance_revision_delete();
DROP TRIGGER user_personal_collection_profiles_advance_revision ON user_personal_collection_profiles;
DROP FUNCTION user_personal_collection_profiles_advance_revision();
DROP TABLE library_collection_revisions;
DROP TABLE user_collection_revisions;
