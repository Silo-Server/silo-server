-- +goose Up
-- Minute-resolution samples for the admin dashboard's concurrent-stream and
-- egress charts. Neither series can be reconstructed after the fact: live
-- sessions leave no per-minute trace and node egress is a rolling average, so
-- the sampler writes them as they happen.
--
-- One row per minute per source. 'shared' is the cluster-wide snapshot (any
-- replica may write it; the primary key makes the first writer win), while
-- 'proc:<node_id>' rows carry the viewer egress served by one API process,
-- which stream_nodes does not cover.
CREATE TABLE IF NOT EXISTS dashboard_metric_samples (
    bucket            timestamptz NOT NULL,
    source            text        NOT NULL,
    streams_total     integer     NOT NULL DEFAULT 0,
    streams_direct    integer     NOT NULL DEFAULT 0,
    streams_remux     integer     NOT NULL DEFAULT 0,
    streams_transcode integer     NOT NULL DEFAULT 0,
    egress_kbps       bigint      NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket, source)
);

-- +goose Down
DROP TABLE IF EXISTS dashboard_metric_samples;
