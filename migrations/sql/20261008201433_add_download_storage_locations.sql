-- +goose Up
-- Measured usage of each prepared-download directory, so every API replica
-- shows the same numbers. node_id NULL is the API server's own directory,
-- reported by each replica under its own reporter id; a node's row is written
-- by whichever replica last listed that node's directory.
CREATE TABLE public.download_storage_samples (
    node_id         integer REFERENCES public.stream_nodes(id) ON DELETE CASCADE,
    reporter        text        NOT NULL DEFAULT '',
    usage           jsonb       NOT NULL,
    untracked_files integer     NOT NULL DEFAULT 0,
    untracked_bytes bigint      NOT NULL DEFAULT 0,
    reconciled_at   timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT download_storage_samples_untracked_check CHECK (untracked_files >= 0 AND untracked_bytes >= 0)
);
CREATE UNIQUE INDEX download_storage_samples_location_uidx
    ON public.download_storage_samples (COALESCE(node_id, 0), reporter);

-- Per-node prepared-download storage. NULL inherits the cluster setting
-- (download.artifact_dir / download.artifact_max_bytes).
ALTER TABLE public.stream_nodes
    ADD COLUMN download_artifact_dir_override text,
    ADD COLUMN download_artifact_max_bytes_override bigint
        CHECK (download_artifact_max_bytes_override >= 0);

COMMENT ON COLUMN public.stream_nodes.download_artifact_dir_override IS
    'download.artifact_dir for this node only. NULL means the node default: '
    'download-artifacts inside its transcode directory, or the cluster setting when set.';
COMMENT ON COLUMN public.stream_nodes.download_artifact_max_bytes_override IS
    'Prepared-download storage budget for this node in bytes; 0 means no budget. '
    'NULL inherits download.artifact_max_bytes.';

-- The configuration revision must move when either new column changes, or an
-- If-Match edit of a node could overwrite a concurrent storage change.
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
    UPDATE stream_node_pool_generation SET generation = generation + 1 WHERE singleton;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
ALTER TABLE public.stream_nodes
    DROP COLUMN IF EXISTS download_artifact_max_bytes_override,
    DROP COLUMN IF EXISTS download_artifact_dir_override;
DROP TABLE IF EXISTS public.download_storage_samples;
