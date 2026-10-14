-- +goose Up
-- Parent indexes are metadata-only here. Startup/migrate-only finishes their
-- leaf indexes concurrently before reporting readiness.
ALTER TABLE activity_log ADD COLUMN action text NOT NULL DEFAULT '',
 ADD COLUMN target_type text NOT NULL DEFAULT '',
 ADD COLUMN target_id text NOT NULL DEFAULT '',
 ADD COLUMN changes jsonb NOT NULL DEFAULT '[]';
CREATE INDEX activity_log_action_time_idx ON ONLY activity_log (action, timestamp DESC, id DESC) WHERE action <> '';
CREATE INDEX activity_log_target_time_idx ON ONLY activity_log (target_type, target_id, timestamp DESC, id DESC) WHERE target_type <> '' OR target_id <> '';

-- +goose Down
DROP INDEX activity_log_target_time_idx;
DROP INDEX activity_log_action_time_idx;
ALTER TABLE activity_log DROP COLUMN changes, DROP COLUMN target_id, DROP COLUMN target_type, DROP COLUMN action;
