-- +goose Up
ALTER TABLE playback_connections
    ADD COLUMN probe_kbps integer CHECK (probe_kbps BETWEEN 1 AND 1000000),
    ADD COLUMN probe_bytes bigint,
    ADD COLUMN probe_duration_ms integer,
    ADD COLUMN probe_transport text CHECK (probe_transport IN ('wifi','ethernet','cellular','other')),
    ADD COLUMN measured_at timestamptz,
    ADD COLUMN plan_summary jsonb,
    ADD COLUMN summary_at timestamptz;

-- +goose Down
ALTER TABLE playback_connections
    DROP COLUMN probe_kbps, DROP COLUMN probe_bytes, DROP COLUMN probe_duration_ms,
    DROP COLUMN probe_transport, DROP COLUMN measured_at,
    DROP COLUMN plan_summary, DROP COLUMN summary_at;
