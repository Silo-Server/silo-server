-- +goose Up
-- admin_stop_commands holds each administrator stop sent to the player and
-- not withdrawn, as {command_id: requested_at}. A player that carries out
-- the stop can send its own stop, and that stop can reach the attempt before
-- the server records the administrator's. The stop that wins the attempt
-- within a short window of any entry copies that into its receipt, so either
-- end records terminated. Each command withdraws only its own entry: when it
-- could not be sent, or the player refused it. Writing it does not touch
-- updated_at, which the idle expiry reads as activity.
ALTER TABLE playback_v3_attempts
    ADD COLUMN IF NOT EXISTS admin_stop_commands JSONB;

-- +goose Down
ALTER TABLE playback_v3_attempts
    DROP COLUMN IF EXISTS admin_stop_commands;
