-- +goose Up
ALTER TABLE public.download_artifacts
    ADD COLUMN origin_node_id integer NOT NULL DEFAULT 0,
    ADD COLUMN origin_node_url text NOT NULL DEFAULT '',
    ADD COLUMN origin_node_group text NOT NULL DEFAULT '',
    ADD COLUMN origin_artifact_id text NOT NULL DEFAULT '',
    ADD CONSTRAINT download_artifacts_origin_check
        CHECK (
            (origin_node_id = 0 AND origin_node_url = '' AND origin_node_group = '' AND origin_artifact_id = '')
            OR
            (origin_node_id > 0 AND origin_node_url <> '' AND origin_artifact_id <> '')
        ) NOT VALID;

-- +goose Down
ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_origin_check,
    DROP COLUMN origin_artifact_id,
    DROP COLUMN origin_node_group,
    DROP COLUMN origin_node_url,
    DROP COLUMN origin_node_id;
