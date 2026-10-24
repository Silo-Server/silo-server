-- +goose Up
-- 20261017230200 added the constraint NOT VALID. Validating takes only a
-- SHARE UPDATE EXCLUSIVE lock, so route events keep flowing during the scan.
ALTER TABLE playback_route_events
    VALIDATE CONSTRAINT playback_route_events_event_check;

-- +goose Down
SELECT 1;
