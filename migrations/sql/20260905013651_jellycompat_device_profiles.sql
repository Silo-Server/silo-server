-- +goose Up
CREATE TABLE jellycompat_device_profiles (
    token_hash text NOT NULL,
    device_id text NOT NULL,
    profile jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (token_hash, device_id)
);
CREATE INDEX jellycompat_device_profiles_expiry_idx ON jellycompat_device_profiles (expires_at);

-- +goose Down
DROP TABLE jellycompat_device_profiles;
