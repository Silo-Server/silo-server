-- +goose Up
-- +goose StatementBegin
ALTER TABLE device_login_requests
    ADD COLUMN client_purpose TEXT NOT NULL DEFAULT 'device_login',
    ADD COLUMN temporary BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN approved_profile_id TEXT;

ALTER TABLE device_login_requests
    ADD CONSTRAINT device_login_requests_client_purpose_check
        CHECK (client_purpose IN ('device_login', 'remote_playback')),
    ADD CONSTRAINT device_login_requests_temporary_purpose_check
        CHECK (temporary = (client_purpose = 'remote_playback')),
    ADD CONSTRAINT device_login_requests_approved_profile_fkey
        FOREIGN KEY (approved_by_user_id, approved_profile_id)
        REFERENCES user_profiles(user_id, id)
        ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE device_login_requests
    DROP CONSTRAINT IF EXISTS device_login_requests_approved_profile_fkey,
    DROP CONSTRAINT IF EXISTS device_login_requests_temporary_purpose_check,
    DROP CONSTRAINT IF EXISTS device_login_requests_client_purpose_check,
    DROP COLUMN IF EXISTS approved_profile_id,
    DROP COLUMN IF EXISTS temporary,
    DROP COLUMN IF EXISTS client_purpose;
-- +goose StatementEnd
