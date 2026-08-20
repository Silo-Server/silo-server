-- +goose Up
-- +goose StatementBegin
-- Protocol v3's neutral contract replaces the Android-shaped
-- output_route_generation counter with an opaque bounded string,
-- output_context_id. v3 is still dark and this contract pass has no
-- back-compat window, so the column is swapped rather than kept alongside.
ALTER TABLE playback_route_events
    DROP COLUMN output_route_generation;
ALTER TABLE playback_route_events
    ADD COLUMN output_context_id TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE playback_route_events
    DROP COLUMN output_context_id;
ALTER TABLE playback_route_events
    ADD COLUMN output_route_generation BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd
