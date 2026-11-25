-- +goose Up
-- A separate migration from the playback_sessions_sync column change in
-- 20261122184925_playback_analytics_plan_sync_and_daily_backfill, so that lock
-- is released before these scans of the minute samples run. Every column
-- takes the larger of the stored and backfilled values, so running it again
-- changes nothing.
-- One-time backfill of all completed UTC days from raw minute samples into the
-- daily archive tables so an upgrade onto replicas with shorter raw retention
-- cannot prune days before the first hourly rollup finishes.
WITH
    _dm_minutes AS (
        SELECT
            (s.bucket AT TIME ZONE 'UTC')::date AS day,
            s.bucket,
            MAX(s.streams_total)     FILTER (WHERE s.source = 'shared') AS streams_total,
            MAX(s.streams_direct)    FILTER (WHERE s.source = 'shared') AS streams_direct,
            MAX(s.streams_remux)     FILTER (WHERE s.source = 'shared') AS streams_remux,
            MAX(s.streams_transcode) FILTER (WHERE s.source = 'shared') AS streams_transcode,
            COALESCE(SUM(s.egress_kbps), 0)::bigint AS egress_kbps,
            COALESCE(SUM(s.download_egress_kbps), 0)::bigint AS download_egress_kbps,
            COALESCE(SUM(s.egress_kbps) FILTER (WHERE s.source LIKE 'proc:%'), 0)::bigint AS api_egress_kbps
        FROM dashboard_metric_samples s
        WHERE s.bucket < (date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
        GROUP BY 1, 2
    ),
    _dm_stream_peaks AS (
        SELECT DISTINCT ON (day)
            day,
            COALESCE(streams_direct, 0)::bigint    AS peak_streams_direct,
            COALESCE(streams_remux, 0)::bigint     AS peak_streams_remux,
            COALESCE(streams_transcode, 0)::bigint AS peak_streams_transcode,
            bucket AS peak_streams_at
        FROM _dm_minutes
        WHERE streams_total IS NOT NULL
        ORDER BY day, streams_total DESC, bucket ASC
    ),
    _dm_egress_peaks AS (
        SELECT DISTINCT ON (day)
            day,
            bucket AS peak_egress_at
        FROM _dm_minutes
        ORDER BY day, egress_kbps DESC, bucket ASC
    ),
    _dm_daily AS (
        SELECT
            m.day,
            COALESCE(MAX(m.streams_total), 0)::bigint          AS peak_streams,
            COALESCE(MAX(m.streams_direct), 0)::bigint         AS peak_direct,
            COALESCE(MAX(m.streams_remux), 0)::bigint          AS peak_remux,
            COALESCE(MAX(m.streams_transcode), 0)::bigint      AS peak_transcode,
            COALESCE(sp.peak_streams_direct, 0)::bigint        AS peak_streams_direct,
            COALESCE(sp.peak_streams_remux, 0)::bigint         AS peak_streams_remux,
            COALESCE(sp.peak_streams_transcode, 0)::bigint     AS peak_streams_transcode,
            COALESCE(MAX(m.egress_kbps), 0)::bigint            AS peak_egress_kbps,
            COALESCE(MAX(m.download_egress_kbps), 0)::bigint   AS peak_download_kbps,
            (COALESCE(SUM(m.egress_kbps), 0) * 7500)::bigint          AS egress_bytes,
            (COALESCE(SUM(m.download_egress_kbps), 0) * 7500)::bigint AS download_egress_bytes,
            (COALESCE(SUM(m.api_egress_kbps), 0) * 7500)::bigint      AS api_egress_bytes,
            COALESCE(MAX(m.api_egress_kbps), 0)::bigint        AS api_peak_egress_kbps,
            sp.peak_streams_at,
            ep.peak_egress_at
        FROM _dm_minutes m
        LEFT JOIN _dm_stream_peaks sp ON sp.day = m.day
        LEFT JOIN _dm_egress_peaks ep ON ep.day = m.day
        GROUP BY m.day, sp.peak_streams_direct, sp.peak_streams_remux, sp.peak_streams_transcode, sp.peak_streams_at, ep.peak_egress_at
    )
INSERT INTO dashboard_metric_daily (
    day, peak_streams, peak_direct, peak_remux, peak_transcode,
    peak_streams_direct, peak_streams_remux, peak_streams_transcode,
    peak_egress_kbps, peak_download_kbps,
    egress_bytes, download_egress_bytes,
    api_egress_bytes, api_peak_egress_kbps,
    peak_streams_at, peak_egress_at, updated_at
)
SELECT
    day, peak_streams::integer, peak_direct::integer, peak_remux::integer, peak_transcode::integer,
    peak_streams_direct::integer, peak_streams_remux::integer, peak_streams_transcode::integer,
    peak_egress_kbps, peak_download_kbps,
    egress_bytes, download_egress_bytes,
    api_egress_bytes, api_peak_egress_kbps,
    peak_streams_at, peak_egress_at, now()
FROM _dm_daily
ON CONFLICT (day) DO UPDATE SET
    peak_streams           = GREATEST(dashboard_metric_daily.peak_streams, EXCLUDED.peak_streams),
    peak_direct            = GREATEST(dashboard_metric_daily.peak_direct, EXCLUDED.peak_direct),
    peak_remux             = GREATEST(dashboard_metric_daily.peak_remux, EXCLUDED.peak_remux),
    peak_transcode         = GREATEST(dashboard_metric_daily.peak_transcode, EXCLUDED.peak_transcode),
    peak_streams_direct    = CASE
        WHEN EXCLUDED.peak_streams > dashboard_metric_daily.peak_streams
            OR (EXCLUDED.peak_streams = dashboard_metric_daily.peak_streams
                AND EXCLUDED.peak_streams_at IS NOT NULL
                AND (dashboard_metric_daily.peak_streams_at IS NULL OR EXCLUDED.peak_streams_at < dashboard_metric_daily.peak_streams_at))
        THEN EXCLUDED.peak_streams_direct ELSE dashboard_metric_daily.peak_streams_direct END,
    peak_streams_remux     = CASE
        WHEN EXCLUDED.peak_streams > dashboard_metric_daily.peak_streams
            OR (EXCLUDED.peak_streams = dashboard_metric_daily.peak_streams
                AND EXCLUDED.peak_streams_at IS NOT NULL
                AND (dashboard_metric_daily.peak_streams_at IS NULL OR EXCLUDED.peak_streams_at < dashboard_metric_daily.peak_streams_at))
        THEN EXCLUDED.peak_streams_remux ELSE dashboard_metric_daily.peak_streams_remux END,
    peak_streams_transcode = CASE
        WHEN EXCLUDED.peak_streams > dashboard_metric_daily.peak_streams
            OR (EXCLUDED.peak_streams = dashboard_metric_daily.peak_streams
                AND EXCLUDED.peak_streams_at IS NOT NULL
                AND (dashboard_metric_daily.peak_streams_at IS NULL OR EXCLUDED.peak_streams_at < dashboard_metric_daily.peak_streams_at))
        THEN EXCLUDED.peak_streams_transcode ELSE dashboard_metric_daily.peak_streams_transcode END,
    peak_egress_kbps       = GREATEST(dashboard_metric_daily.peak_egress_kbps, EXCLUDED.peak_egress_kbps),
    peak_download_kbps     = GREATEST(dashboard_metric_daily.peak_download_kbps, EXCLUDED.peak_download_kbps),
    egress_bytes           = GREATEST(dashboard_metric_daily.egress_bytes, EXCLUDED.egress_bytes),
    download_egress_bytes  = GREATEST(dashboard_metric_daily.download_egress_bytes, EXCLUDED.download_egress_bytes),
    api_egress_bytes       = GREATEST(dashboard_metric_daily.api_egress_bytes, EXCLUDED.api_egress_bytes),
    api_peak_egress_kbps   = GREATEST(dashboard_metric_daily.api_peak_egress_kbps, EXCLUDED.api_peak_egress_kbps),
    peak_streams_at        = CASE
        WHEN EXCLUDED.peak_streams > dashboard_metric_daily.peak_streams
            OR (EXCLUDED.peak_streams = dashboard_metric_daily.peak_streams
                AND EXCLUDED.peak_streams_at IS NOT NULL
                AND (dashboard_metric_daily.peak_streams_at IS NULL OR EXCLUDED.peak_streams_at < dashboard_metric_daily.peak_streams_at))
        THEN EXCLUDED.peak_streams_at ELSE dashboard_metric_daily.peak_streams_at END,
    peak_egress_at         = CASE
        WHEN EXCLUDED.peak_egress_kbps > dashboard_metric_daily.peak_egress_kbps
            OR (EXCLUDED.peak_egress_kbps = dashboard_metric_daily.peak_egress_kbps
                AND EXCLUDED.peak_egress_at IS NOT NULL
                AND (dashboard_metric_daily.peak_egress_at IS NULL OR EXCLUDED.peak_egress_at < dashboard_metric_daily.peak_egress_at))
        THEN EXCLUDED.peak_egress_at ELSE dashboard_metric_daily.peak_egress_at END,
    updated_at             = now();

INSERT INTO dashboard_node_egress_daily (day, node_id, node_name, node_type, egress_bytes, peak_egress_kbps, updated_at)
SELECT
    (s.bucket AT TIME ZONE 'UTC')::date AS day,
    s.node_id,
    COALESCE(NULLIF(BTRIM(sn.name), ''), '') AS node_name,
    sn.type AS node_type,
    (COALESCE(SUM(s.egress_kbps), 0) * 7500)::bigint AS egress_bytes,
    COALESCE(MAX(s.egress_kbps), 0)::bigint          AS peak_egress_kbps,
    now()
FROM dashboard_node_egress_samples s
LEFT JOIN stream_nodes sn ON sn.id = s.node_id
WHERE s.bucket < (date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
GROUP BY 1, 2, 3, 4
ON CONFLICT (day, node_id) DO UPDATE SET
    node_name        = COALESCE(NULLIF(BTRIM(dashboard_node_egress_daily.node_name), ''), EXCLUDED.node_name),
    node_type        = COALESCE(dashboard_node_egress_daily.node_type, EXCLUDED.node_type),
    egress_bytes     = GREATEST(dashboard_node_egress_daily.egress_bytes, EXCLUDED.egress_bytes),
    peak_egress_kbps = GREATEST(dashboard_node_egress_daily.peak_egress_kbps, EXCLUDED.peak_egress_kbps),
    updated_at       = now();

-- +goose Down
-- Nothing to undo: the hourly rollup keeps the same daily rows.
