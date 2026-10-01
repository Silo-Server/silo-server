-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN capability_drift_baseline jsonb;

COMMENT ON COLUMN stream_nodes.capability_drift_baseline IS
    'Machine-readable record of what capability_drift is standing for: '
    '{"backends": ["qsv"], "devices": [["GPU-uuid", "0000:03:00.0", '
    '"/dev/dri/renderD128"]]} — the backends that must verify again and the '
    'device alias sets one of whose members must reappear before the note is '
    'cleared. Each device is stored as every stable name it answered to, so a '
    'card that comes back renumbered, or whose nvidia-smi uuid is missing on '
    'the pass that finds it, still matches. NULL exactly when capability_drift '
    'is NULL. It exists because recovery cannot be derived from the stored '
    'report alone: once a degraded report is stored, every later comparison is '
    'degraded-to-degraded and finds nothing lost, and mere growth in the '
    'inventory is not recovery either — adding an unrelated GPU to a node that '
    'lost one is not the lost one returning. Written by the node health sweep '
    'in the same statement as capabilities and capability_drift, so it always '
    'describes the note beside it. Not a routing input.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS capability_drift_baseline;
-- +goose StatementEnd
