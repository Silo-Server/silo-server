-- +goose Up
ALTER TABLE stream_nodes ADD COLUMN drain_revision bigint NOT NULL DEFAULT 0;
CREATE TABLE stream_node_drain_fences (
    node_id integer PRIMARY KEY REFERENCES stream_nodes(id) ON DELETE CASCADE,
    fence_id text NOT NULL UNIQUE,
    requested_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION enforce_stream_node_drain_fence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.enabled AND EXISTS (SELECT 1 FROM stream_node_drain_fences WHERE node_id=NEW.id) THEN
        RAISE EXCEPTION 'node retirement must be canceled before enabling'
            USING ERRCODE = '23514', CONSTRAINT = 'stream_node_drain_requires_disabled';
    END IF;
    IF ROW(NEW.url,NEW.type,NEW.name) IS DISTINCT FROM ROW(OLD.url,OLD.type,OLD.name)
       AND EXISTS (SELECT 1 FROM stream_node_drain_fences WHERE node_id=NEW.id) THEN
        RAISE EXCEPTION 'node retirement must be canceled before changing worker identity'
            USING ERRCODE = '23514', CONSTRAINT = 'stream_node_drain_target_fenced';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER stream_node_drain_requires_disabled BEFORE UPDATE ON stream_nodes
FOR EACH ROW EXECUTE FUNCTION enforce_stream_node_drain_fence();

-- +goose Down
-- Serialize with the production node-row -> fence write order before checking
-- emptiness. Otherwise a BeginDrain can publish after the check but before DDL.
LOCK TABLE stream_nodes IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM stream_node_drain_fences) THEN
        RAISE EXCEPTION 'cancel worker drains before rolling back this migration';
    END IF;
END $$;
-- +goose StatementEnd
-- Keep the last effective validator when the older binary resumes reading
-- admin_revision alone. Its normal no-change trigger would otherwise overwrite
-- this promotion; the exclusive lock prevents any writer during the transfer.
ALTER TABLE stream_nodes DISABLE TRIGGER stream_node_configuration_revision;
UPDATE stream_nodes SET admin_revision=GREATEST(admin_revision,drain_revision);
ALTER TABLE stream_nodes ENABLE TRIGGER stream_node_configuration_revision;
DROP TRIGGER stream_node_drain_requires_disabled ON stream_nodes;
DROP FUNCTION enforce_stream_node_drain_fence();
DROP TABLE stream_node_drain_fences;
ALTER TABLE stream_nodes DROP COLUMN drain_revision;
