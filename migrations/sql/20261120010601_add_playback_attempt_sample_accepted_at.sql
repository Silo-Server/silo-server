-- +goose Up
-- sample_accepted_at is when the attempt accepted last_sample: a progress
-- report, or a stop's final sample newer than the last report. It is the
-- accepting API server's clock, the clock the session's watch-time
-- measurement uses. updated_at can't stand in for it, since replan commits,
-- stops, expiry and finalization also move it. A stop finalized from this row
-- judges the sample against this time, so a seek before the stop is not
-- counted as watch time. NULL for samples accepted before this column existed.
ALTER TABLE playback_v3_attempts ADD COLUMN IF NOT EXISTS sample_accepted_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE playback_v3_attempts DROP COLUMN IF EXISTS sample_accepted_at;
