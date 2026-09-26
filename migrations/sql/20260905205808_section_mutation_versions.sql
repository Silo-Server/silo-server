-- +goose Up
CREATE TABLE page_section_revisions (
       section_id TEXT PRIMARY KEY,
       revision BIGINT NOT NULL DEFAULT 1
);
CREATE TABLE page_section_scope_revisions (
       scope TEXT NOT NULL,
       library_id INTEGER NOT NULL DEFAULT 0,
       revision BIGINT NOT NULL DEFAULT 1,
       PRIMARY KEY (scope, library_id)
);
INSERT INTO page_section_revisions(section_id)
SELECT id FROM page_sections;
INSERT INTO page_section_scope_revisions(scope, library_id)
SELECT DISTINCT scope, COALESCE(library_id, 0) FROM page_sections;
CREATE INDEX page_sections_scope_position_id ON page_sections(scope, library_id, position, id);

-- +goose StatementBegin
CREATE FUNCTION page_sections_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO page_section_scope_revisions(scope, library_id, revision)
    SELECT scope, COALESCE(library_id, 0), 2 FROM (SELECT id, scope, library_id FROM new_rows) affected
    GROUP BY scope, COALESCE(library_id, 0)
    ORDER BY scope, COALESCE(library_id, 0)
    ON CONFLICT (scope, library_id) DO UPDATE SET revision = page_section_scope_revisions.revision + 1;
    INSERT INTO page_section_revisions(section_id, revision)
    SELECT id, 2 FROM (SELECT id, scope, library_id FROM new_rows) affected GROUP BY id ORDER BY id
    ON CONFLICT (section_id) DO UPDATE SET revision = page_section_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER page_sections_revision_insert AFTER INSERT ON page_sections
    REFERENCING NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION page_sections_revision_insert();

-- +goose StatementBegin
CREATE FUNCTION page_sections_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO page_section_scope_revisions(scope, library_id, revision)
    SELECT scope, COALESCE(library_id, 0), 2 FROM (SELECT id, scope, library_id FROM old_rows UNION SELECT id, scope, library_id FROM new_rows) affected
    GROUP BY scope, COALESCE(library_id, 0)
    ORDER BY scope, COALESCE(library_id, 0)
    ON CONFLICT (scope, library_id) DO UPDATE SET revision = page_section_scope_revisions.revision + 1;
    INSERT INTO page_section_revisions(section_id, revision)
    SELECT id, 2 FROM (SELECT id, scope, library_id FROM old_rows UNION SELECT id, scope, library_id FROM new_rows) affected GROUP BY id ORDER BY id
    ON CONFLICT (section_id) DO UPDATE SET revision = page_section_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER page_sections_revision_update AFTER UPDATE ON page_sections
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION page_sections_revision_update();

-- +goose StatementBegin
CREATE FUNCTION page_sections_revision_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO page_section_scope_revisions(scope, library_id, revision)
    SELECT scope, COALESCE(library_id, 0), 2 FROM (SELECT id, scope, library_id FROM old_rows) affected
    GROUP BY scope, COALESCE(library_id, 0)
    ORDER BY scope, COALESCE(library_id, 0)
    ON CONFLICT (scope, library_id) DO UPDATE SET revision = page_section_scope_revisions.revision + 1;
    INSERT INTO page_section_revisions(section_id, revision)
    SELECT id, 2 FROM (SELECT id, scope, library_id FROM old_rows) affected GROUP BY id ORDER BY id
    ON CONFLICT (section_id) DO UPDATE SET revision = page_section_revisions.revision + 1;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER page_sections_revision_delete AFTER DELETE ON page_sections
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT EXECUTE FUNCTION page_sections_revision_delete();

-- +goose Down
DROP TRIGGER page_sections_revision_insert ON page_sections;
DROP FUNCTION page_sections_revision_insert();
DROP TRIGGER page_sections_revision_update ON page_sections;
DROP FUNCTION page_sections_revision_update();
DROP TRIGGER page_sections_revision_delete ON page_sections;
DROP FUNCTION page_sections_revision_delete();
DROP INDEX page_sections_scope_position_id;
DROP TABLE page_section_revisions;
DROP TABLE page_section_scope_revisions;
