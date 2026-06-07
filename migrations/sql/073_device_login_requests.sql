-- +goose Up
-- +goose StatementBegin
CREATE TABLE device_login_requests (
    id UUID PRIMARY KEY,
    device_code_hash TEXT NOT NULL UNIQUE,
    browser_code_hash TEXT NOT NULL UNIQUE,
    user_code_hash TEXT NOT NULL UNIQUE,
    match_code TEXT NOT NULL,
    device_name TEXT NOT NULL,
    device_platform TEXT NOT NULL DEFAULT '',
    ip_address INET,
    requested_user_agent TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    approved_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    auth_session_id TEXT REFERENCES auth_sessions(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    approved_at TIMESTAMPTZ,
    denied_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT device_login_requests_status_check
        CHECK (status IN ('pending', 'approved', 'denied', 'consumed'))
);

CREATE INDEX idx_device_login_requests_expires_at
    ON device_login_requests (expires_at);

CREATE INDEX idx_device_login_requests_status_expires_at
    ON device_login_requests (status, expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS device_login_requests;
-- +goose StatementEnd
