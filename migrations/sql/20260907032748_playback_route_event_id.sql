-- +goose Up
-- A route event reported through the v2 surface carries a client-minted event
-- id so a retry after a lost 202 records the event once. Legacy v1 events have
-- no id and stay unconstrained (NULL never collides in a unique index).
ALTER TABLE playback_route_events ADD COLUMN event_id UUID;
CREATE UNIQUE INDEX playback_route_events_event_id_key
    ON playback_route_events (playback_attempt_id, event_id)
    WHERE event_id IS NOT NULL;

-- +goose Down
DROP INDEX playback_route_events_event_id_key;
ALTER TABLE playback_route_events DROP COLUMN event_id;
