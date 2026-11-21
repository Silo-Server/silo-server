-- +goose Up
-- A node can be pinned to VideoToolbox, as the cluster-wide playback.hw_accel
-- already can: hw_accel_override takes the same values as that setting.
ALTER TABLE public.stream_nodes DROP CONSTRAINT stream_nodes_hw_accel_override_check;
ALTER TABLE public.stream_nodes
    ADD CONSTRAINT stream_nodes_hw_accel_override_check
        CHECK (hw_accel_override IN ('auto', 'qsv', 'vaapi', 'nvenc', 'videotoolbox', 'none'));

-- +goose Down
-- A node pinned to VideoToolbox goes back to inheriting the cluster setting,
-- and a field its environment declared is no longer locked to that value.
UPDATE public.stream_nodes
SET hw_accel_override = NULL,
    env_managed = array_remove(env_managed, 'hw_accel_override')
WHERE hw_accel_override = 'videotoolbox';
ALTER TABLE public.stream_nodes DROP CONSTRAINT stream_nodes_hw_accel_override_check;
ALTER TABLE public.stream_nodes
    ADD CONSTRAINT stream_nodes_hw_accel_override_check
        CHECK (hw_accel_override IN ('auto', 'qsv', 'vaapi', 'nvenc', 'none'));
