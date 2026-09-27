-- +goose Up
CREATE INDEX user_devices_profile_recency_idx ON user_devices (user_id, profile_id, last_seen_at DESC, device_id);
CREATE INDEX user_devices_household_recency_idx ON user_devices (user_id, last_seen_at DESC, profile_id, device_id);

-- +goose Down
DROP INDEX user_devices_household_recency_idx;
DROP INDEX user_devices_profile_recency_idx;
