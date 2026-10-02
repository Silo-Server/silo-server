-- +goose Up
-- Split the sampled egress series: egress_kbps stays the total viewer egress a
-- source served (unchanged semantics, so existing charts stay truthful), and
-- download_egress_kbps carries the subset served by file-transfer routes
-- (offline downloads, direct downloads, ebook/ABS file fetches) — telemetry
-- ClassTransfer traffic, as opposed to streaming playback.
--
-- Rows written before this migration report 0 downloads, which reads as "the
-- split was not measured yet", never as inflated playback: playback is derived
-- as egress_kbps - download_egress_kbps.
--
-- The table is minute-resolution and pruned to a 31-day window (a few hundred
-- thousand rows at most), so a plain in-transaction ALTER is safe.
ALTER TABLE dashboard_metric_samples
    ADD COLUMN IF NOT EXISTS download_egress_kbps bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE dashboard_metric_samples
    DROP COLUMN IF EXISTS download_egress_kbps;
