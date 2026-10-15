-- +goose Up
-- node_key_ciphertext is the transcode node's own request-signing key,
-- encrypted with the data cipher under AAD stream_nodes.node_key/<id>. NULL
-- means no key has been issued. It is a credential, not configuration:
-- track_stream_node_configuration does not compare it, so rotating a key does
-- not move the node's admin revision.
--
-- source_access is how a transcode node reads source media: 'local' at the
-- catalog paths through its own mount, or 'core' over HTTP from the API.
ALTER TABLE public.stream_nodes
    ADD COLUMN node_key_ciphertext text,
    ADD COLUMN source_access text NOT NULL DEFAULT 'local'
        CONSTRAINT stream_nodes_source_access_check CHECK (source_access IN ('local', 'core'));

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
    -- This update commits or rolls back with the configuration write. It is a
    -- durable invalidation marker, not an acknowledgement from every replica.
    UPDATE stream_node_pool_generation SET generation = generation + 1 WHERE singleton;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
ALTER TABLE public.stream_nodes
    DROP COLUMN IF EXISTS source_access,
    DROP COLUMN IF EXISTS node_key_ciphertext;
