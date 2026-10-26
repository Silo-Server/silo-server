-- +goose Up
-- Live state of the current prepare attempt, written by the lease owner so
-- every API replica can show it. A claim resets all of it.
ALTER TABLE public.download_artifacts
    ADD COLUMN started_at timestamptz,                       -- when the current attempt was claimed
    ADD COLUMN worker_kind text NOT NULL DEFAULT '',         -- '' until chosen, then 'server' or 'node'
    ADD COLUMN worker_node_id integer,                       -- stream_nodes.id when worker_kind = 'node'
    ADD COLUMN worker_name text NOT NULL DEFAULT '',         -- node name or API node id when chosen
    ADD COLUMN progress_encoded_seconds double precision,
    ADD COLUMN progress_duration_seconds double precision,
    ADD COLUMN progress_speed double precision,
    ADD COLUMN progress_updated_at timestamptz,
    ADD COLUMN progress_unavailable boolean NOT NULL DEFAULT false, -- the worker cannot report progress
    ADD CONSTRAINT download_artifacts_worker_check
        CHECK (worker_kind IN ('', 'server', 'node') AND (worker_kind = 'node') = (worker_node_id IS NOT NULL));

-- Unfinished and failed rows: the admin preparation list and its counts. Ready
-- rows are the bulk of the table and are never listed.
CREATE INDEX download_artifacts_unready_idx ON public.download_artifacts (created_at)
    WHERE status NOT IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready');

-- +goose Down
DROP INDEX public.download_artifacts_unready_idx;
ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_worker_check,
    DROP COLUMN progress_unavailable,
    DROP COLUMN progress_updated_at,
    DROP COLUMN progress_speed,
    DROP COLUMN progress_duration_seconds,
    DROP COLUMN progress_encoded_seconds,
    DROP COLUMN worker_name,
    DROP COLUMN worker_node_id,
    DROP COLUMN worker_kind,
    DROP COLUMN started_at;
