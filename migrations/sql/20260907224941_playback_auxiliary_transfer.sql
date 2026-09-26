-- +goose Up
-- A separate namespace prevents an executor-output permit from being presented
-- as API producer authority. The selected proxy owns its exact response permit.
CREATE TABLE playback_auxiliary_transfer_permits (
    permit_id UUID PRIMARY KEY,
    playback_attempt_id TEXT NOT NULL REFERENCES playback_v3_attempts(playback_attempt_id) ON DELETE CASCADE,
    incarnation UUID NOT NULL,
    owner_id UUID NOT NULL,
    epoch BIGINT NOT NULL CHECK (epoch > 0),
    session_id UUID NOT NULL,
    plan_id TEXT NOT NULL,
    transport_id TEXT NOT NULL,
    executor JSONB NOT NULL,
    egress_node_id BIGINT NOT NULL CHECK (egress_node_id > 0)
);
CREATE INDEX playback_auxiliary_transfer_attempt ON playback_auxiliary_transfer_permits(playback_attempt_id);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM playback_auxiliary_transfer_permits) THEN
        RAISE EXCEPTION 'Cannot remove auxiliary transfer permits while bindings remain';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE playback_auxiliary_transfer_permits;
