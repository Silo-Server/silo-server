-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN hw_accel_override text
        CHECK (hw_accel_override IN ('auto', 'qsv', 'vaapi', 'nvenc', 'none')),
    ADD COLUMN hw_device_override text;

COMMENT ON COLUMN stream_nodes.hw_accel_override IS
    'Per-node hardware acceleration backend, overriding the cluster-wide '
    'playback.hw_accel setting for this node only. NULL means inherit the '
    'cluster value — the normal case for a homogeneous deployment. Set it '
    'when one node''s hardware differs from the rest (a CPU-only box in a QSV '
    'cluster sets ''none''). The node reads its own row (matched on url '
    'against its NODE_URL) on every config reload, so this is the value it '
    'probes with and falls back to; the API dispatches remote transcodes with '
    'it too, in preference to the cluster value. A change applies without a '
    'restart, except to the boot-time encoder warmup and to sessions already '
    'transcoding.';

COMMENT ON COLUMN stream_nodes.hw_device_override IS
    'Per-node hardware device (render node path or index) for the backend '
    'above, overriding the cluster-wide playback.hw_device for this node '
    'only. NULL means inherit the cluster value. Applies on the same terms as '
    'hw_accel_override.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS hw_accel_override,
    DROP COLUMN IF EXISTS hw_device_override;
-- +goose StatementEnd
