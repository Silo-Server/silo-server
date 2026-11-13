-- +goose Up
-- checkpoint: where a run's listing stands, so a restart continues near where
-- it stopped. resume_attempts counts restarts without progress, so a run that
-- keeps killing the process stops being retried. large_import marks the one
-- very large run allowed at a time, across every node.
ALTER TABLE history_import_runs
    ADD COLUMN checkpoint jsonb,
    ADD COLUMN resume_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN large_import boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX history_import_one_large_run
    ON history_import_runs ((true))
    WHERE large_import AND status = 'running';

-- +goose Down
DROP INDEX history_import_one_large_run;
ALTER TABLE history_import_runs
    DROP COLUMN large_import,
    DROP COLUMN resume_attempts,
    DROP COLUMN checkpoint;
