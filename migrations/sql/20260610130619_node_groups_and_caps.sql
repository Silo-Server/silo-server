-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN node_group text,
    ADD COLUMN max_jobs integer,
    ADD COLUMN max_bandwidth_kbps integer,
    ADD COLUMN egress_kbps integer NOT NULL DEFAULT 0;

COMMENT ON COLUMN stream_nodes.node_group IS
    'Optional co-location group. Nodes sharing a group are assumed to be on the '
    'same host/LAN. A group is only eligible for selection while every enabled '
    'member is healthy; transcoded streams are served by a proxy from the same '
    'group as the chosen transcode node.';

COMMENT ON COLUMN stream_nodes.max_jobs IS
    'Maximum concurrent jobs for this node (transcodes for transcode nodes, '
    'streams for proxy nodes). NULL = unlimited.';

COMMENT ON COLUMN stream_nodes.max_bandwidth_kbps IS
    'Maximum egress bandwidth for proxy nodes in kilobits/s. New streams are '
    'not admitted once the measured egress would exceed this. NULL = unlimited.';

COMMENT ON COLUMN stream_nodes.egress_kbps IS
    'Last health-reported egress bandwidth (rolling average, kilobits/s). '
    'Currently only proxy nodes report a non-zero value.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS node_group,
    DROP COLUMN IF EXISTS max_jobs,
    DROP COLUMN IF EXISTS max_bandwidth_kbps,
    DROP COLUMN IF EXISTS egress_kbps;
-- +goose StatementEnd
