-- +goose Up
-- +goose StatementBegin
INSERT INTO server_settings (key, value)
VALUES ('playback.protocol_v3_enabled', 'false')
ON CONFLICT (key) DO NOTHING;

INSERT INTO server_settings (key, value)
VALUES ('playback.protocol_v3_shadow_enabled', 'false')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE playback_v3_attempts (
    playback_attempt_id TEXT PRIMARY KEY,
    session_id UUID NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    requested_media_file_id BIGINT NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    effective_media_file_id BIGINT NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    current_plan_id TEXT NOT NULL,
    current_plan JSONB NOT NULL,
    normalized_request JSONB NOT NULL,
    request_digest TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (jsonb_typeof(current_plan) = 'object'),
    CHECK (jsonb_typeof(normalized_request) = 'object')
);

CREATE INDEX playback_v3_attempts_expiry_idx
    ON playback_v3_attempts (expires_at);

CREATE TABLE playback_v3_replans (
    session_id UUID NOT NULL REFERENCES playback_v3_attempts(session_id) ON UPDATE CASCADE ON DELETE CASCADE,
    replan_request_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    lease_expires_at TIMESTAMPTZ NOT NULL,
    response JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, replan_request_id),
    CHECK (state IN ('active', 'completed')),
    CHECK (response IS NULL OR jsonb_typeof(response) = 'object')
);

CREATE INDEX playback_v3_replans_lease_idx
    ON playback_v3_replans (state, lease_expires_at);

CREATE TABLE playback_route_events (
    id BIGSERIAL PRIMARY KEY,
    playback_attempt_id TEXT NOT NULL,
    session_id UUID,
    plan_id TEXT,
    plan_attempt_id TEXT,
    plan_attempt_key TEXT,
    event TEXT NOT NULL,
    failure_classification TEXT,
    fallback_reason TEXT,
    output_route_generation BIGINT NOT NULL DEFAULT 0,
    diagnostics JSONB NOT NULL DEFAULT '{}'::jsonb,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    client_name TEXT,
    client_version TEXT,
    client_model TEXT,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (event IN (
        'plan_selected', 'plan_invalidated', 'plan_failed', 'first_frame',
        'terminal', 'stopped', 'runtime_correction_applied',
        'runtime_correction_succeeded', 'runtime_correction_failed',
        'seek_reanchor_requested', 'seek_reanchored'
    )),
    CHECK (jsonb_typeof(diagnostics) = 'object')
);

CREATE INDEX playback_route_events_attempt_idx
    ON playback_route_events (playback_attempt_id, received_at);
CREATE INDEX playback_route_events_release_idx
    ON playback_route_events (event, failure_classification, received_at);
-- The hourly retention delete scans by age alone; without this index it is a
-- sequential scan over up to 30 days of telemetry.
CREATE INDEX playback_route_events_received_idx
    ON playback_route_events (received_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS playback_route_events;
DROP TABLE IF EXISTS playback_v3_replans;
DROP TABLE IF EXISTS playback_v3_attempts;
DELETE FROM server_settings WHERE key IN ('playback.protocol_v3_enabled', 'playback.protocol_v3_shadow_enabled');
-- +goose StatementEnd
