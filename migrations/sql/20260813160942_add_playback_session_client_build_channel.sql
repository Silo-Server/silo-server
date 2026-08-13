-- +goose Up
-- Client build number and release channel are reported by the app alongside its
-- version, so admin activity and route diagnostics can name the exact build
-- ("Silo Android TV 1.0.0 (build 5)") instead of a version alone. Both are
-- opaque, per-platform strings the server never parses.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS client_build text,
    ADD COLUMN IF NOT EXISTS client_channel text;

ALTER TABLE public.playback_route_events
    ADD COLUMN IF NOT EXISTS client_build text,
    ADD COLUMN IF NOT EXISTS client_channel text;

-- +goose Down
ALTER TABLE public.playback_route_events
    DROP COLUMN IF EXISTS client_channel,
    DROP COLUMN IF EXISTS client_build;

ALTER TABLE public.playback_sessions_sync
    DROP COLUMN IF EXISTS client_channel,
    DROP COLUMN IF EXISTS client_build;
