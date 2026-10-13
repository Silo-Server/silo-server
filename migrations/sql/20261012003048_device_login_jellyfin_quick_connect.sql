-- +goose Up
-- +goose StatementBegin
-- Jellyfin Quick Connect requests share the device sign-in table. They are
-- never temporary: the session lasts like any other sign-in, bound to the
-- profile the approver chose.
ALTER TABLE device_login_requests
    DROP CONSTRAINT device_login_requests_client_purpose_check,
    ADD CONSTRAINT device_login_requests_client_purpose_check
        CHECK (client_purpose IN ('device_login', 'remote_playback', 'jellyfin_quick_connect'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Quick Connect requests live at most 30 minutes; the older constraint
-- cannot hold them.
DELETE FROM device_login_requests WHERE client_purpose = 'jellyfin_quick_connect';
ALTER TABLE device_login_requests
    DROP CONSTRAINT device_login_requests_client_purpose_check,
    ADD CONSTRAINT device_login_requests_client_purpose_check
        CHECK (client_purpose IN ('device_login', 'remote_playback'));
-- +goose StatementEnd
