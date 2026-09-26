-- +goose Up
CREATE INDEX policy_versions_author_document_idx ON policy_document_versions (created_by_user_id, document_id)
WHERE created_by_user_id IS NOT NULL;

-- Account deletion performs ON DELETE SET NULL on attributed versions. Lock
-- their parents before the FK takes version-row locks, matching document
-- deletion. Version creation acquires the author's key-share lock first, so
-- it cannot introduce a new attributed version after this parent scan.
-- +goose StatementBegin
CREATE FUNCTION lock_policy_documents_before_author_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM d.id FROM policy_documents d
    WHERE EXISTS (
        SELECT 1 FROM policy_document_versions v
        WHERE v.document_id = d.id AND v.created_by_user_id = OLD.id
    )
    ORDER BY d.id FOR UPDATE;
    RETURN OLD;
END;
$$;
CREATE TRIGGER policy_author_document_locks BEFORE DELETE ON users
FOR EACH ROW EXECUTE FUNCTION lock_policy_documents_before_author_delete();
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER policy_author_document_locks ON users;
DROP FUNCTION lock_policy_documents_before_author_delete();
DROP INDEX policy_versions_author_document_idx;
