-- +goose Up
-- How far a request target's downloads are, as its router plugin last reported
-- them (request_router.reports_download_progress). Set only while the target is
-- queued or downloading, and cleared when it completes or fails.
-- download_updated_at is when the server last heard from the plugin, and
-- download_checked_at when a pass last asked about the target, answered or
-- not; the download refresh pass takes targets in that order. A progress write
-- leaves updated_at alone: that column dates the target's last status change,
-- and the stalled-target backstop reads it.
ALTER TABLE media_request_targets
    ADD COLUMN download_phase text,
    ADD COLUMN download_bytes_total bigint
        CONSTRAINT media_request_targets_download_bytes_total_check CHECK (download_bytes_total >= 0),
    ADD COLUMN download_bytes_left bigint
        CONSTRAINT media_request_targets_download_bytes_left_check CHECK (download_bytes_left >= 0),
    ADD COLUMN download_eta timestamptz,
    ADD COLUMN download_count integer,
    ADD COLUMN download_updated_at timestamptz,
    ADD COLUMN download_checked_at timestamptz;

-- +goose Down
ALTER TABLE media_request_targets
    DROP COLUMN download_checked_at,
    DROP COLUMN download_updated_at,
    DROP COLUMN download_count,
    DROP COLUMN download_eta,
    DROP COLUMN download_bytes_left,
    DROP COLUMN download_bytes_total,
    DROP COLUMN download_phase;
