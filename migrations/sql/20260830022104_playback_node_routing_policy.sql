-- +goose Up
-- The node id columns are deliberately unconstrained. This table is a sync
-- projection the reconciler re-upserts from in-memory sessions every tick, all
-- of one reporting node's rows inside one transaction; a foreign key would turn
-- an admin deleting a referenced stream node into a repeating violation that
-- aborts session sync for every session on that node. The admin sessions loader
-- already LEFT JOINs by id and renders a dangling reference as no node name.
ALTER TABLE playback_sessions_sync
    ADD COLUMN routing_workload text,
    ADD COLUMN routing_execution text,
    ADD COLUMN routing_execution_node_id integer,
    ADD COLUMN routing_execution_node_url text,
    ADD COLUMN routing_egress text,
    ADD COLUMN routing_egress_node_id integer,
    ADD COLUMN routing_egress_node_url text;

INSERT INTO server_settings (key, value)
SELECT 'playback.routing.remux_execution', 'worker_only'
WHERE EXISTS (
    SELECT 1
    FROM server_settings
    WHERE key = 'playback.local_transcode_fallback'
      AND lower(trim(value)) = 'false'
)
ON CONFLICT (key) DO NOTHING;

INSERT INTO server_settings (key, value)
SELECT 'playback.routing.video_transcode_execution', 'worker_only'
WHERE EXISTS (
    SELECT 1
    FROM server_settings
    WHERE key = 'playback.local_transcode_fallback'
      AND lower(trim(value)) = 'false'
)
ON CONFLICT (key) DO NOTHING;

-- Prepared downloads are deliberately outside playback routing. Preserve the
-- old shared false override under a download-owned key before removing it.
INSERT INTO server_settings (key, value)
SELECT 'download.local_transcode_fallback', 'false'
WHERE EXISTS (
    SELECT 1
    FROM server_settings
    WHERE key = 'playback.local_transcode_fallback'
      AND lower(trim(value)) = 'false'
)
ON CONFLICT (key) DO NOTHING;

DELETE FROM server_settings
WHERE key = 'playback.local_transcode_fallback';

-- +goose Down
ALTER TABLE playback_sessions_sync
    DROP COLUMN IF EXISTS routing_egress_node_url,
    DROP COLUMN IF EXISTS routing_egress_node_id,
    DROP COLUMN IF EXISTS routing_egress,
    DROP COLUMN IF EXISTS routing_execution_node_url,
    DROP COLUMN IF EXISTS routing_execution_node_id,
    DROP COLUMN IF EXISTS routing_execution,
    DROP COLUMN IF EXISTS routing_workload;

INSERT INTO server_settings (key, value)
SELECT 'playback.local_transcode_fallback', 'false'
WHERE EXISTS (
    SELECT 1
    FROM server_settings
    WHERE (
        key IN (
            'playback.routing.remux_execution',
            'playback.routing.video_transcode_execution'
        )
        AND value = 'worker_only'
    ) OR (
        key = 'download.local_transcode_fallback'
        AND lower(trim(value)) = 'false'
    )
)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

DELETE FROM server_settings
WHERE key IN (
    'playback.routing.direct_play_egress',
    'playback.routing.remux_execution',
    'playback.routing.remux_egress',
    'playback.routing.video_transcode_execution',
    'playback.routing.video_transcode_egress',
    'download.local_transcode_fallback'
);
