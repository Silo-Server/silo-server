-- +goose Up
-- +goose StatementBegin
-- source_access is how a transcode node reads its source media: 'local' through
-- its own library mount at the core's paths, or 'core' over HTTP from the API
-- servers, so a node needs no library mount. It reaches the node in its
-- configuration document, so it is configuration: the trigger below compares
-- it, and a change moves admin_revision and the pool generation. A proxy reads
-- no sources, so it can only hold the default.
ALTER TABLE public.stream_nodes
    ADD COLUMN source_access text NOT NULL DEFAULT 'local'
        CONSTRAINT stream_nodes_source_access_check
            CHECK (source_access IN ('local', 'core') AND (type = 'transcode' OR source_access = 'local'));

CREATE OR REPLACE FUNCTION public.track_stream_node_configuration()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.name, NEW.type, NEW.url, NEW.public_url, NEW.enabled,
               NEW.node_group, NEW.max_jobs, NEW.max_bandwidth_kbps,
               NEW.hw_accel_override, NEW.hw_device_override,
               NEW.download_artifact_dir_override, NEW.download_artifact_max_bytes_override,
               NEW.source_access)
           IS NOT DISTINCT FROM
           ROW(OLD.name, OLD.type, OLD.url, OLD.public_url, OLD.enabled,
               OLD.node_group, OLD.max_jobs, OLD.max_bandwidth_kbps,
               OLD.hw_accel_override, OLD.hw_device_override,
               OLD.download_artifact_dir_override, OLD.download_artifact_max_bytes_override,
               OLD.source_access) THEN
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.track_stream_node_configuration()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.name, NEW.type, NEW.url, NEW.public_url, NEW.enabled,
               NEW.node_group, NEW.max_jobs, NEW.max_bandwidth_kbps,
               NEW.hw_accel_override, NEW.hw_device_override,
               NEW.download_artifact_dir_override, NEW.download_artifact_max_bytes_override)
           IS NOT DISTINCT FROM
           ROW(OLD.name, OLD.type, OLD.url, OLD.public_url, OLD.enabled,
               OLD.node_group, OLD.max_jobs, OLD.max_bandwidth_kbps,
               OLD.hw_accel_override, OLD.hw_device_override,
               OLD.download_artifact_dir_override, OLD.download_artifact_max_bytes_override) THEN
            NEW.admin_revision := OLD.admin_revision;
            RETURN NEW;
        END IF;
        NEW.admin_revision := nextval('stream_node_admin_revision_seq');
    END IF;
    UPDATE stream_node_pool_generation SET generation = generation + 1 WHERE singleton;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE public.stream_nodes
    DROP COLUMN IF EXISTS source_access;
-- +goose StatementEnd
