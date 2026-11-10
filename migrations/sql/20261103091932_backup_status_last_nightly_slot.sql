-- +goose Up
-- The scheduled slot (the daily backup.time at or before the run started)
-- of the latest successful nightly backup, from whichever API server took
-- it. A scheduled run on another server skips a slot already backed up, so
-- one night's trigger firing on every server takes one backup.
ALTER TABLE backup_status ADD COLUMN IF NOT EXISTS last_nightly_slot timestamptz;

-- +goose Down
ALTER TABLE backup_status DROP COLUMN IF EXISTS last_nightly_slot;
