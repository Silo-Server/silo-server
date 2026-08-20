-- +goose Up
-- Attempts created before the neutral contract do not carry its finalized
-- evidence or replay semantics. They are short-lived session state, so force
-- those clients through a clean start after deploy instead of decoding an
-- incompatible stored request during a replan.
DELETE FROM playback_v3_attempts;

-- +goose Down
-- Playback attempts are ephemeral and cannot be reconstructed safely.
SELECT 1;
