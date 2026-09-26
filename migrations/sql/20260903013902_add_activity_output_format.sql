-- +goose Up
-- Transport-selected output facts; NULL remains unknown on older nodes.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS output_container text,
    ADD COLUMN IF NOT EXISTS output_protocol text;

-- +goose Down
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN IF EXISTS output_container,
    DROP COLUMN IF EXISTS output_protocol;
