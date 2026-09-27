-- +goose Up
-- Executor grant acquisition starts from a signed namespace, not an attempt ID.
-- Null legacy incarnations are omitted from this lookup and uniqueness rule.
CREATE UNIQUE INDEX playback_v3_attempts_control_incarnation_idx
    ON playback_v3_attempts (control_incarnation)
    WHERE control_incarnation IS NOT NULL;

-- +goose Down
DROP INDEX playback_v3_attempts_control_incarnation_idx;
