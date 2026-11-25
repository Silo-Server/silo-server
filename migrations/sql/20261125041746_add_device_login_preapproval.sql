-- +goose Up
-- A device sign-in approved in advance from the account's own session: the
-- row is inserted already approved, bound to the device's fingerprint, and
-- waits up to 30 days for the device to poll it. preapproved_at marks such a
-- row; client_purpose stays device_login so the existing poll redeems it.
-- The existing index on approved_by_user_id serves the per-account list and
-- cap.
ALTER TABLE device_login_requests
    ADD COLUMN preapproved_at TIMESTAMPTZ,
    ADD CONSTRAINT device_login_requests_preapproved_bound CHECK (
        preapproved_at IS NULL
        OR (client_purpose = 'device_login' AND temporary = FALSE AND binding_hash IS NOT NULL)
    );

-- +goose Down
ALTER TABLE device_login_requests
    DROP CONSTRAINT IF EXISTS device_login_requests_preapproved_bound,
    DROP COLUMN IF EXISTS preapproved_at;
