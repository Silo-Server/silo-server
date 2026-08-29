-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN capabilities jsonb,
    ADD COLUMN capabilities_hash text,
    ADD COLUMN capabilities_refreshed_at timestamptz;

COMMENT ON COLUMN stream_nodes.capabilities IS
    'Last capability report fetched from this node (the /hw-capabilities '
    'payload: resolved backend, render devices with PCI address and GPU uuid, '
    'probed backends, transformations, tone-map executors). NULL until the '
    'node has advertised a capability hash and the fetch succeeded. Durable '
    'so GPU inventory survives an API restart and so a node that goes '
    'unhealthy still reports what hardware it had.';

COMMENT ON COLUMN stream_nodes.capabilities_hash IS
    'Identity of the stored capabilities payload, as computed by the node. '
    'The health sweep refetches only when the node reports a hash different '
    'from this one, so an unchanged node costs one health request. NULL until '
    'the first successful fetch.';

COMMENT ON COLUMN stream_nodes.capabilities_refreshed_at IS
    'When the stored capabilities were last fetched and persisted. This is the '
    'age of the inventory, not of the last health check; a node checked every '
    '30s may keep capabilities from hours ago because nothing changed.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS capabilities,
    DROP COLUMN IF EXISTS capabilities_hash,
    DROP COLUMN IF EXISTS capabilities_refreshed_at;
-- +goose StatementEnd
