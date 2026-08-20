-- +goose Up
CREATE TABLE public.download_artifact_orphans (
    id bigserial PRIMARY KEY,
    download_artifact_id text NOT NULL,
    origin_node_id integer NOT NULL,
    origin_node_url text NOT NULL,
    origin_artifact_id text NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_retry_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (origin_node_id, origin_artifact_id),
    CONSTRAINT download_artifact_orphans_locator_check
        CHECK (download_artifact_id <> '' AND origin_node_id > 0 AND origin_node_url <> '' AND origin_artifact_id <> '')
);

CREATE INDEX download_artifact_orphans_due_idx
    ON public.download_artifact_orphans (next_retry_at, created_at);

-- +goose Down
DROP TABLE IF EXISTS public.download_artifact_orphans;
