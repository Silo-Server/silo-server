-- +goose Up
ALTER TABLE admin_jobs
 ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false,
 ADD COLUMN claim_generation bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE admin_jobs DROP COLUMN cancel_requested, DROP COLUMN claim_generation;
