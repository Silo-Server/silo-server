-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN capability_drift text;

COMMENT ON COLUMN stream_nodes.capability_drift IS
    'Human-readable note describing how this node''s hardware got worse, set '
    'when a capability refetch shows a previously verified backend now failing '
    'its probe or a render device that has disappeared. It stays set until a '
    'refetch produces a report whose probes all pass: a refetch that merely '
    'loses nothing further leaves it alone, because a comparison against an '
    'already-degraded report always finds nothing and would report a still- '
    'broken node as repaired. NULL therefore means no standing regression. It '
    'exists because that regression is otherwise only a log line: a driver that '
    'stopped working turns a node from a GPU transcoder into a silent CPU one, '
    'and the node stays healthy throughout. Written only by the node health '
    'sweep, in the same statement as capabilities and capabilities_hash, so it '
    'always describes the report stored beside it. Not a routing input.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS capability_drift;
-- +goose StatementEnd
