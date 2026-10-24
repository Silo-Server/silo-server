-- +goose Up
CREATE TABLE playback_connections (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id text NOT NULL,
    connection_key text NOT NULL,
    limit_kbps integer CHECK (limit_kbps BETWEEN 100 AND 1000000),
    observed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_id, connection_key)
);
CREATE INDEX playback_connections_updated_at ON playback_connections(updated_at);
-- A device may be registered under several household profiles. Forget its
-- observations only after its last registration disappears.
-- +goose StatementBegin
CREATE FUNCTION delete_playback_device_connections() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM user_devices WHERE user_id=OLD.user_id AND device_id=OLD.device_id) THEN
        DELETE FROM playback_connections WHERE user_id=OLD.user_id AND device_id=OLD.device_id;
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER delete_playback_device_connections AFTER DELETE ON user_devices
FOR EACH ROW EXECUTE FUNCTION delete_playback_device_connections();

-- NOT VALID keeps the ACCESS EXCLUSIVE lock to the catalog change: checking
-- the retained route events here would block playback reads and writes for
-- the whole scan. A later migration validates it under a lighter lock.
ALTER TABLE playback_route_events DROP CONSTRAINT playback_route_events_event_check;
ALTER TABLE playback_route_events ADD CONSTRAINT playback_route_events_event_check CHECK (event IN ('plan_selected', 'plan_invalidated', 'plan_failed', 'first_frame', 'terminal', 'stopped', 'runtime_correction_applied', 'runtime_correction_succeeded', 'runtime_correction_failed', 'seek_reanchor_requested', 'seek_reanchored', 'plan_summary')) NOT VALID;

-- +goose Down
DELETE FROM playback_route_events WHERE event='plan_summary';
ALTER TABLE playback_route_events DROP CONSTRAINT playback_route_events_event_check;
ALTER TABLE playback_route_events ADD CONSTRAINT playback_route_events_event_check CHECK (event IN ('plan_selected', 'plan_invalidated', 'plan_failed', 'first_frame', 'terminal', 'stopped', 'runtime_correction_applied', 'runtime_correction_succeeded', 'runtime_correction_failed', 'seek_reanchor_requested', 'seek_reanchored'));
DROP TRIGGER delete_playback_device_connections ON user_devices;
DROP FUNCTION delete_playback_device_connections();
DROP TABLE playback_connections;
