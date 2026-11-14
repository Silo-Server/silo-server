-- +goose Up
-- Per-node breakdown of the node egress the dashboard samples already carry in
-- their 'shared' row. It lives in its own table rather than as extra
-- dashboard_metric_samples rows because readers sum every row of a minute in
-- that table: a server running an older build (mid rolling upgrade, or after a
-- rollback) would otherwise count node egress twice for the whole retention
-- window. No foreign key to stream_nodes: a deleted node's history stays
-- readable until it ages out with the rest of the samples.
CREATE TABLE IF NOT EXISTS dashboard_node_egress_samples (
    bucket      timestamptz NOT NULL,
    node_id     integer     NOT NULL,
    egress_kbps bigint      NOT NULL,
    PRIMARY KEY (bucket, node_id)
);

-- +goose Down
DROP TABLE IF EXISTS dashboard_node_egress_samples;
