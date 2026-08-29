-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN last_stats jsonb;

COMMENT ON COLUMN stream_nodes.last_stats IS
    'Most recent host resource sample reported by this node in its health '
    'response: {"system": {cpu_pct, load1, cores, mem_used_mb, mem_total_mb, '
    'disks, net_rx_bps, net_tx_bps}, "gpu": [{device, vendor, sessions, '
    'video_busy_pct, render_busy_pct, total_busy_pct, vram_used_mb, '
    'vram_total_mb, source}]}. Written by the same 30s health update that '
    'writes active_jobs, so it is exactly as old as last_health_check. '
    'Current sample only — this is not a history table; operators who want '
    'trends scrape the node /metrics endpoint. NULL when the node reported no '
    'sample: a build predating resource sampling, a non-Linux host, or a node '
    'that failed its health check.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS last_stats;
-- +goose StatementEnd
