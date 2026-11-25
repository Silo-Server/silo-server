-- +goose Up
-- Enrich finalized playback history with the delivered route facts that live
-- sessions already hold (and drop at teardown), plus daily downsampled tables
-- that preserve peak concurrent streams and server/node egress past the 31-day
-- retention window of dashboard_metric_samples.
--
-- admin_playback_history stays unpartitioned so RecordHistory's
-- ON CONFLICT (session_id) upserts and advancing ended_at continue to work.
-- route_facts_recorded distinguishes post-upgrade direct-play rows (whose
-- transcode columns are legitimately NULL) from pre-upgrade rows so Insights
-- can report honest telemetry coverage boundaries.
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    ADD COLUMN IF NOT EXISTS target_video_codec TEXT,
    ADD COLUMN IF NOT EXISTS target_resolution TEXT,
    ADD COLUMN IF NOT EXISTS target_audio_codec TEXT,
    ADD COLUMN IF NOT EXISTS target_audio_channels INTEGER,
    ADD COLUMN IF NOT EXISTS output_container TEXT,
    ADD COLUMN IF NOT EXISTS output_protocol TEXT,
    ADD COLUMN IF NOT EXISTS target_bitrate_kbps INTEGER,
    ADD COLUMN IF NOT EXISTS transcode_hw_accel TEXT,
    ADD COLUMN IF NOT EXISTS tone_map_mode TEXT,
    ADD COLUMN IF NOT EXISTS adaptation_reasons TEXT[],
    ADD COLUMN IF NOT EXISTS reporting_node TEXT,
    ADD COLUMN IF NOT EXISTS stream_location TEXT,
    ADD COLUMN IF NOT EXISTS compat_origin BOOLEAN,
    ADD COLUMN IF NOT EXISTS routing_execution TEXT,
    ADD COLUMN IF NOT EXISTS routing_execution_node_id INTEGER,
    ADD COLUMN IF NOT EXISTS routing_egress TEXT,
    ADD COLUMN IF NOT EXISTS routing_egress_node_id INTEGER,
    ADD COLUMN IF NOT EXISTS route_facts_recorded BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS dashboard_metric_daily (
    day                    DATE        PRIMARY KEY,
    peak_streams           INTEGER     NOT NULL DEFAULT 0,
    peak_direct            INTEGER     NOT NULL DEFAULT 0,
    peak_remux             INTEGER     NOT NULL DEFAULT 0,
    peak_transcode         INTEGER     NOT NULL DEFAULT 0,
    peak_streams_direct    INTEGER     NOT NULL DEFAULT 0,
    peak_streams_remux     INTEGER     NOT NULL DEFAULT 0,
    peak_streams_transcode INTEGER     NOT NULL DEFAULT 0,
    peak_egress_kbps       BIGINT      NOT NULL DEFAULT 0,
    peak_download_kbps     BIGINT      NOT NULL DEFAULT 0,
    egress_bytes           BIGINT      NOT NULL DEFAULT 0,
    download_egress_bytes  BIGINT      NOT NULL DEFAULT 0,
    api_egress_bytes       BIGINT      NOT NULL DEFAULT 0,
    api_peak_egress_kbps   BIGINT      NOT NULL DEFAULT 0,
    peak_streams_at        TIMESTAMPTZ,
    peak_egress_at         TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-node daily egress archive mirroring dashboard_node_egress_samples so
-- multi-year bandwidth breakdowns outlive the 31-day minute sample retention.
-- No foreign key to stream_nodes: deleted nodes stay readable as historical
-- sources.
CREATE TABLE IF NOT EXISTS dashboard_node_egress_daily (
    day              DATE        NOT NULL,
    node_id          INTEGER     NOT NULL,
    egress_bytes     BIGINT      NOT NULL DEFAULT 0,
    peak_egress_kbps BIGINT      NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (day, node_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS dashboard_node_egress_daily;
DROP TABLE IF EXISTS dashboard_metric_daily;

ALTER TABLE admin_playback_history
    DROP COLUMN IF EXISTS route_facts_recorded,
    DROP COLUMN IF EXISTS routing_egress_node_id,
    DROP COLUMN IF EXISTS routing_egress,
    DROP COLUMN IF EXISTS routing_execution_node_id,
    DROP COLUMN IF EXISTS routing_execution,
    DROP COLUMN IF EXISTS compat_origin,
    DROP COLUMN IF EXISTS stream_location,
    DROP COLUMN IF EXISTS reporting_node,
    DROP COLUMN IF EXISTS adaptation_reasons,
    DROP COLUMN IF EXISTS tone_map_mode,
    DROP COLUMN IF EXISTS transcode_hw_accel,
    DROP COLUMN IF EXISTS target_bitrate_kbps,
    DROP COLUMN IF EXISTS output_protocol,
    DROP COLUMN IF EXISTS output_container,
    DROP COLUMN IF EXISTS target_audio_channels,
    DROP COLUMN IF EXISTS target_audio_codec,
    DROP COLUMN IF EXISTS target_resolution,
    DROP COLUMN IF EXISTS target_video_codec;
-- +goose StatementEnd
