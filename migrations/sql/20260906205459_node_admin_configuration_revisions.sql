-- +goose Up
-- Configuration validators deliberately exclude health and capability samples.
CREATE SEQUENCE stream_node_admin_revision_seq;
ALTER TABLE stream_nodes ADD COLUMN admin_revision bigint NOT NULL
    DEFAULT nextval('stream_node_admin_revision_seq');
CREATE TABLE stream_node_pool_generation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    generation bigint NOT NULL
);
INSERT INTO stream_node_pool_generation VALUES (true, 1);

-- +goose StatementBegin
CREATE FUNCTION track_stream_node_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.name, NEW.type, NEW.url, NEW.public_url, NEW.enabled,
               NEW.node_group, NEW.max_jobs, NEW.max_bandwidth_kbps,
               NEW.hw_accel_override, NEW.hw_device_override)
           IS NOT DISTINCT FROM
           ROW(OLD.name, OLD.type, OLD.url, OLD.public_url, OLD.enabled,
               OLD.node_group, OLD.max_jobs, OLD.max_bandwidth_kbps,
               OLD.hw_accel_override, OLD.hw_device_override) THEN
            NEW.admin_revision := OLD.admin_revision;
            RETURN NEW;
        END IF;
        NEW.admin_revision := nextval('stream_node_admin_revision_seq');
    END IF;
    -- This update commits or rolls back with the configuration write. It is a
    -- durable invalidation marker, not an acknowledgement from every replica.
    UPDATE stream_node_pool_generation SET generation = generation + 1 WHERE singleton;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER stream_node_configuration_revision
BEFORE INSERT OR UPDATE OR DELETE ON stream_nodes
FOR EACH ROW EXECUTE FUNCTION track_stream_node_configuration();

-- +goose Down
DROP TRIGGER stream_node_configuration_revision ON stream_nodes;
DROP FUNCTION track_stream_node_configuration();
DROP TABLE stream_node_pool_generation;
ALTER TABLE stream_nodes DROP COLUMN admin_revision;
DROP SEQUENCE stream_node_admin_revision_seq;
