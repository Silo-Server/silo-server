-- +goose Up
ALTER TABLE policy_documents ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);

-- +goose StatementBegin
CREATE FUNCTION advance_policy_document_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := OLD.revision + 1;
    RETURN NEW;
END;
$$;
CREATE TRIGGER policy_document_revision BEFORE UPDATE ON policy_documents
FOR EACH ROW EXECUTE FUNCTION advance_policy_document_revision();

-- A document's editor witness includes its immutable version history. Foreign
-- key maintenance of version metadata must invalidate the same witness too.
CREATE FUNCTION advance_policy_version_parent_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE parent_id bigint;
BEGIN
    FOR parent_id IN
        SELECT DISTINCT id FROM unnest(ARRAY[OLD.document_id, NEW.document_id]) AS parents(id)
        WHERE id IS NOT NULL ORDER BY id
    LOOP
        UPDATE policy_documents SET revision = revision WHERE id = parent_id;
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE TRIGGER policy_version_parent_revision AFTER INSERT OR UPDATE OR DELETE ON policy_document_versions
FOR EACH ROW EXECUTE FUNCTION advance_policy_version_parent_revision();
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER policy_version_parent_revision ON policy_document_versions;
DROP FUNCTION advance_policy_version_parent_revision();
DROP TRIGGER policy_document_revision ON policy_documents;
DROP FUNCTION advance_policy_document_revision();
ALTER TABLE policy_documents DROP COLUMN revision;
