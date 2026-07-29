-- +goose Up
ALTER TABLE movie_match_queue
    ADD COLUMN rerun_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN lease_forced_rerun boolean NOT NULL DEFAULT false;

ALTER TABLE series_root_match_queue
    ADD COLUMN rerun_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN lease_forced_rerun boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE series_root_match_queue
    DROP COLUMN lease_forced_rerun,
    DROP COLUMN rerun_requested;

ALTER TABLE movie_match_queue
    DROP COLUMN lease_forced_rerun,
    DROP COLUMN rerun_requested;
