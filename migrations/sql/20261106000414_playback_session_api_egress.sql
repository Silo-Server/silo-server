-- API-served bytes per playback session: the viewer bytes the API processes
-- themselves sent for a play, summed from each process's per-minute
-- stream-telemetry deltas (internal/dashmetrics). Bytes a stream node or proxy
-- sends a viewer directly are not counted, hence the name.
--
-- It is its own table rather than a column on admin_playback_history because
-- bytes arrive for the whole play, while the history row only exists once the
-- play ends. Readers join it to the history row by session_id. No foreign key:
-- the history row usually arrives later. The dashboard sampler deletes a row
-- that has no history row once it has gone 31 days without new bytes.

-- +goose Up
CREATE TABLE IF NOT EXISTS playback_session_api_egress (
    session_id       text        PRIMARY KEY,
    api_served_bytes bigint      NOT NULL,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS playback_session_api_egress;
