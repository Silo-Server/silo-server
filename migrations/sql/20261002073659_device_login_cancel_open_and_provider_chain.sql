-- +goose Up
-- +goose StatementBegin
-- Device sign-in (docs/architecture/device-login.md).
--
-- A device can withdraw its own pending or approved-but-uncollected request
-- (canceled), and the first approver lookup is recorded (opened_at) so the
-- device can show that someone is acting on its code and stop replacing it.
--
-- A device sign-in approved from a session opened through an external
-- sign-in provider hands that session's provider chain to the session the
-- device receives (docs/architecture/external-sign-in.md, "Provider
-- re-check"): the identity it came from (approved_identity_id) and when the
-- provider last vouched for the chain (approved_provider_since).
ALTER TABLE device_login_requests
    DROP CONSTRAINT device_login_requests_status_check,
    ADD CONSTRAINT device_login_requests_status_check
        CHECK (status IN ('pending', 'approved', 'denied', 'consumed', 'canceled')),
    ADD COLUMN opened_at TIMESTAMPTZ,
    ADD COLUMN canceled_at TIMESTAMPTZ,
    ADD COLUMN approved_identity_id BIGINT REFERENCES plugin_auth_identities(id) ON DELETE SET NULL,
    ADD COLUMN approved_provider_since TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE device_login_requests SET status = 'denied' WHERE status = 'canceled';

ALTER TABLE device_login_requests
    DROP CONSTRAINT device_login_requests_status_check,
    ADD CONSTRAINT device_login_requests_status_check
        CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
    DROP COLUMN IF EXISTS approved_provider_since,
    DROP COLUMN IF EXISTS approved_identity_id,
    DROP COLUMN IF EXISTS canceled_at,
    DROP COLUMN IF EXISTS opened_at;
-- +goose StatementEnd
