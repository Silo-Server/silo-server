-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN node_group text,
    ADD COLUMN max_jobs integer;

COMMENT ON COLUMN stream_nodes.node_group IS
    'Optional co-location group. Nodes sharing a group are assumed to be on the '
    'same host/LAN. A group is only eligible for selection while every enabled '
    'member is healthy; transcoded streams are served by a proxy from the same '
    'group as the chosen transcode node.';

COMMENT ON COLUMN stream_nodes.max_jobs IS
    'Maximum concurrent jobs for this node (transcodes for transcode nodes, '
    'streams for proxy nodes). NULL = unlimited.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS node_group,
    DROP COLUMN IF EXISTS max_jobs;
-- +goose StatementEnd
