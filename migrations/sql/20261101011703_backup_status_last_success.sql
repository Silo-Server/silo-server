-- +goose Up
-- When the latest successful backup finished, at whichever destination it
-- went to. last_run_* holds only the latest run, which can be a failure,
-- and the folders listed come from the current destination only, so after
-- switching destinations neither says whether Silo backed up recently.
ALTER TABLE backup_status ADD COLUMN IF NOT EXISTS last_success_at timestamptz;

-- +goose Down
ALTER TABLE backup_status DROP COLUMN IF EXISTS last_success_at;
