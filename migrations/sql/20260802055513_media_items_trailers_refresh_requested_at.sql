-- +goose Up
-- Cooldown state for the viewer-facing "find trailers" action. The refresh
-- debt queue cannot hold it: MarkTargetSuccess deletes the row once the reason
-- mask clears, so its last_attempt_at evaporates exactly on success. NULL
-- means "never requested"; the request path's atomic check-and-set writes
-- NOW() only when the stored timestamp is older than the cooldown window.
ALTER TABLE media_items
    ADD COLUMN trailers_refresh_requested_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE media_items
    DROP COLUMN trailers_refresh_requested_at;
