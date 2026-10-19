-- +goose Up
-- A quick speed test that proved the server's target reports only a lower
-- bound, and playback reports the best throughput it reached, also a lower
-- bound on the connection's speed.
ALTER TABLE playback_connections
    ADD COLUMN probe_lower_bound boolean NOT NULL DEFAULT false,
    ADD COLUMN sample_kbps integer CHECK (sample_kbps BETWEEN 1 AND 1000000),
    ADD COLUMN sample_at timestamptz;

-- +goose Down
ALTER TABLE playback_connections
    DROP COLUMN probe_lower_bound, DROP COLUMN sample_kbps, DROP COLUMN sample_at;
