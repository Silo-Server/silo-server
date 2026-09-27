-- +goose Up
CREATE TABLE library_collection_order_revisions (
 library_id INTEGER PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 1
);
INSERT INTO library_collection_order_revisions(library_id) SELECT id FROM media_folders;
-- +goose StatementBegin
CREATE FUNCTION library_collection_libraries_order_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision)
 SELECT library_id,2 FROM (SELECT library_id FROM new_rows) affected GROUP BY library_id ORDER BY library_id
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_libraries_order_revision_insert AFTER INSERT ON library_collection_libraries
 REFERENCING NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION library_collection_libraries_order_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION library_collection_libraries_order_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision)
 SELECT library_id,2 FROM (SELECT library_id FROM old_rows UNION SELECT library_id FROM new_rows) affected GROUP BY library_id ORDER BY library_id
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_libraries_order_revision_update AFTER UPDATE ON library_collection_libraries
 REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION library_collection_libraries_order_revision_update();
-- +goose StatementBegin
CREATE FUNCTION library_collection_libraries_order_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision)
 SELECT library_id,2 FROM (SELECT library_id FROM old_rows) affected GROUP BY library_id ORDER BY library_id
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_libraries_order_revision_delete AFTER DELETE ON library_collection_libraries
 REFERENCING OLD TABLE AS old_rows
 FOR EACH STATEMENT EXECUTE FUNCTION library_collection_libraries_order_revision_delete();
-- +goose StatementBegin
CREATE FUNCTION library_collection_groups_order_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision)
 SELECT library_id,2 FROM (SELECT library_id FROM new_rows) affected GROUP BY library_id ORDER BY library_id
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_groups_order_revision_insert AFTER INSERT ON library_collection_groups
 REFERENCING NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION library_collection_groups_order_revision_insert();
-- +goose StatementBegin
CREATE FUNCTION library_collection_groups_order_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision)
 SELECT library_id,2 FROM (SELECT library_id FROM old_rows UNION SELECT library_id FROM new_rows) affected GROUP BY library_id ORDER BY library_id
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_groups_order_revision_update AFTER UPDATE ON library_collection_groups
 REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
 FOR EACH STATEMENT EXECUTE FUNCTION library_collection_groups_order_revision_update();
-- +goose StatementBegin
CREATE FUNCTION library_collection_groups_order_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision)
 SELECT library_id,2 FROM (SELECT library_id FROM old_rows) affected GROUP BY library_id ORDER BY library_id
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_groups_order_revision_delete AFTER DELETE ON library_collection_groups
 REFERENCING OLD TABLE AS old_rows
 FOR EACH STATEMENT EXECUTE FUNCTION library_collection_groups_order_revision_delete();
-- +goose StatementBegin
CREATE FUNCTION library_collection_ungrouped_order_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO library_collection_order_revisions(library_id,revision) VALUES(NEW.id,2)
 ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER library_collection_ungrouped_order_revision AFTER UPDATE OF collection_ungrouped_sort_order ON media_folders
 FOR EACH ROW WHEN (OLD.collection_ungrouped_sort_order IS DISTINCT FROM NEW.collection_ungrouped_sort_order)
 EXECUTE FUNCTION library_collection_ungrouped_order_revision();

-- +goose Down
DROP TRIGGER library_collection_ungrouped_order_revision ON media_folders;
DROP FUNCTION library_collection_ungrouped_order_revision();
DROP TRIGGER library_collection_libraries_order_revision_insert ON library_collection_libraries;
DROP FUNCTION library_collection_libraries_order_revision_insert();
DROP TRIGGER library_collection_libraries_order_revision_update ON library_collection_libraries;
DROP FUNCTION library_collection_libraries_order_revision_update();
DROP TRIGGER library_collection_libraries_order_revision_delete ON library_collection_libraries;
DROP FUNCTION library_collection_libraries_order_revision_delete();
DROP TRIGGER library_collection_groups_order_revision_insert ON library_collection_groups;
DROP FUNCTION library_collection_groups_order_revision_insert();
DROP TRIGGER library_collection_groups_order_revision_update ON library_collection_groups;
DROP FUNCTION library_collection_groups_order_revision_update();
DROP TRIGGER library_collection_groups_order_revision_delete ON library_collection_groups;
DROP FUNCTION library_collection_groups_order_revision_delete();
DROP TABLE library_collection_order_revisions;
