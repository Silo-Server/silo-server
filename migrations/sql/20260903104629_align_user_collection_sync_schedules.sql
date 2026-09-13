-- +goose Up
-- Personal collection cadence labels used to persist at 04:30 UTC. Align the
-- exact legacy values with the equivalent server-collection presets. Keep the
-- already-computed next_sync_at so upgrades do not make every affected
-- collection immediately due; the following successful run computes the next
-- occurrence from the new schedule.
UPDATE user_personal_collections
SET sync_schedule = CASE sync_schedule
        WHEN '30 4 * * *' THEN '0 3 * * *'
        WHEN '30 4 * * 0' THEN '0 3 * * 0'
        WHEN '30 4 1 * *' THEN '0 3 1 * *'
    END,
    updated_at = NOW()
WHERE sync_schedule IN ('30 4 * * *', '30 4 * * 0', '30 4 1 * *');

-- +goose Down
-- No-op: the Up migration cannot distinguish its rewritten rows from rows
-- created or edited with the same 03:00 schedules afterward. Rewriting every
-- matching row on rollback would corrupt those newer user choices.
SELECT 1;
