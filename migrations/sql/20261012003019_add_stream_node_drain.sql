-- +goose Up
-- +goose StatementBegin
-- drain_requested is an administrator's request to stop placing new work on
-- the node while its existing sessions finish. Unlike enabled = false, a
-- draining node stays in the pools, so its health checks and job counts keep
-- running and the admin page can say when it is empty. It is an operational
-- state, not configuration: track_stream_node_configuration does not compare
-- it, so draining does not move the node's admin revision.
--
-- drain_state is what the node itself said on its last health check: 'draining'
-- while it refuses new work and finishes what it has (after a drain request or
-- SIGTERM), 'drained' once nothing is left, and 'none' otherwise, including for
-- a node that predates draining. Rewritten by every health check, like healthy
-- and active_jobs.
ALTER TABLE stream_nodes
    ADD COLUMN drain_requested BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN drain_state TEXT NOT NULL DEFAULT 'none'
        CONSTRAINT stream_nodes_drain_state_check CHECK (drain_state IN ('none', 'draining', 'drained'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS drain_state,
    DROP COLUMN IF EXISTS drain_requested;
-- +goose StatementEnd
