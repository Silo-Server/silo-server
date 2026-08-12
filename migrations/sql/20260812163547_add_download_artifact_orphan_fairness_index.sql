-- +goose Up
CREATE INDEX download_artifact_orphans_origin_fairness_idx
    ON public.download_artifact_orphans (origin_node_id, origin_node_url, attempts, created_at, id);

-- +goose Down
DROP INDEX IF EXISTS public.download_artifact_orphans_origin_fairness_idx;
