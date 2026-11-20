-- +goose Up
-- admin_stop_command_id names an administrator's stop that was sent to the
-- player at admin_stop_requested_at and not refused. A player that carries
-- out the stop can send its own stop, and that stop can reach the attempt
-- before the server records the administrator's. The stop that wins the
-- attempt within a short window of the request copies the mark into its
-- receipt, so either end records terminated. Only the command that set the
-- mark clears it: when it could not be sent, or the player refused it.
-- Writing them does not touch updated_at, which the idle expiry reads as
-- activity.
ALTER TABLE playback_v3_attempts
    ADD COLUMN IF NOT EXISTS admin_stop_command_id TEXT,
    ADD COLUMN IF NOT EXISTS admin_stop_requested_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE playback_v3_attempts
    DROP COLUMN IF EXISTS admin_stop_requested_at,
    DROP COLUMN IF EXISTS admin_stop_command_id;
