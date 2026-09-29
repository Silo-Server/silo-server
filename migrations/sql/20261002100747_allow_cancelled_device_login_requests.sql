-- +goose NO TRANSACTION
-- +goose Up
-- A device can withdraw its own pending sign-in request (cancelDeviceLogin).
ALTER TABLE device_login_requests
    DROP CONSTRAINT device_login_requests_status_check,
    ADD CONSTRAINT device_login_requests_status_check
        CHECK (status IN ('pending', 'approved', 'denied', 'consumed', 'cancelled'))
        NOT VALID;

ALTER TABLE device_login_requests
    VALIDATE CONSTRAINT device_login_requests_status_check;

-- +goose Down
-- Application versions from this one on write cancelled. Keep accepting it
-- rather than rewriting requests the device already withdrew.
SELECT 1;
