-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_status_check,
    ADD CONSTRAINT history_import_runs_status_check
        CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled'))
        NOT VALID;

ALTER TABLE history_import_runs
    VALIDATE CONSTRAINT history_import_runs_status_check;

-- +goose Down
-- Previous application versions already write cancelled. Keep accepting it
-- without rewriting terminal runs or stranding pending cancellation requests.
SELECT 1;
