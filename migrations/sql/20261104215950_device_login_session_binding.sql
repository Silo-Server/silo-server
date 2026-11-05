-- +goose Up
-- +goose StatementBegin
-- Device sign-in session binding (docs/architecture/device-login.md,
-- "Session binding").
--
-- A device may start a request with a binding, base64url (no padding) of
-- SHA-256 of a secret it keeps. Every poll of such a request must present the
-- secret, so a copy of the binding, which getDeviceLogin shows the approver,
-- cannot collect tokens. NULL is a request started without one.
--
-- A node from an older release collects without checking the secret. Only
-- this release sets binding_checked when it collects, so the check refuses an
-- older node's collection of a bound request, and the session it inserted in
-- the same transaction rolls back with it.
ALTER TABLE device_login_requests
    ADD COLUMN binding_hash TEXT
        CONSTRAINT device_login_requests_binding_hash_check
        CHECK (binding_hash ~ '^[A-Za-z0-9_-]{43}$'),
    ADD COLUMN binding_checked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT device_login_requests_bound_collection_check
        CHECK (binding_hash IS NULL OR status <> 'consumed' OR binding_checked);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Without the column a bound request could be collected without its secret,
-- so live ones are refused first.
UPDATE device_login_requests
SET status = 'denied', denied_at = NOW(), updated_at = NOW()
WHERE binding_hash IS NOT NULL AND status IN ('pending', 'approved');

ALTER TABLE device_login_requests
    DROP CONSTRAINT IF EXISTS device_login_requests_bound_collection_check,
    DROP COLUMN IF EXISTS binding_checked,
    DROP COLUMN IF EXISTS binding_hash;
-- +goose StatementEnd
